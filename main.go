package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/pkg/errors"
	"golang.org/x/exp/slices"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func main() {
	ctx := context.Background()
	c, err := NewRuntimeClientFromFileName(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}

	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "eksa-system"}}); err != nil && !apierrors.IsAlreadyExists(err) {
		log.Fatal(err)
	}

	nodes, err := getControlPlaneNodes(ctx, c)
	if err != nil {
		log.Fatal(err)
	}

	firstCPNode := nodes[0]
	if err := upgradeFirstControlPlaneNode(ctx, c, &firstCPNode); err != nil {
		log.Fatal(err)
	}

	for _, node := range nodes[1:] {
		n := node
		if err := upgradeRestControlPlaneNode(ctx, c, &n); err != nil {
			log.Fatal(err)
		}
	}
}

func getControlPlaneNodes(ctx context.Context, c client.Client) ([]corev1.Node, error) {
	nodes := &corev1.NodeList{}
	if err := c.List(ctx, nodes, client.HasLabels{"node-role.kubernetes.io/control-plane"}); err != nil {
		return nil, err
	}

	// Sort nodes by name to have reproducible logic
	slices.SortFunc(nodes.Items, func(a, b corev1.Node) int {
		if a.Name < b.Name {
			return 1
		} else if a.Name > b.Name {
			return -1
		}

		return 0
	})

	return nodes.Items, nil
}

func upgradeFirstControlPlaneNode(ctx context.Context, c client.Client, node *corev1.Node) error {
	upgrader := upgradeFirstControlPlanePod(node.Name)
	return upgradeNode(ctx, c, node, upgrader)
}

func upgradeRestControlPlaneNode(ctx context.Context, c client.Client, node *corev1.Node) error {
	upgrader := upgradeRestControlPlanePod(node.Name)
	return upgradeNode(ctx, c, node, upgrader)
}

const targetVersion = "v1.27.4-eks-cedffd4"

func upgradeNode(ctx context.Context, c client.Client, node *corev1.Node, upgrader *corev1.Pod) error {
	if node.Status.NodeInfo.KubeletVersion == targetVersion {
		fmt.Printf("Node %s has already been upgraded\n", node.Name)
		return nil
	}

	fmt.Printf("Upgrading node %s\n", node.Name)
	key := client.ObjectKeyFromObject(upgrader)
	pod := &corev1.Pod{}

	if err := c.Get(ctx, key, pod); apierrors.IsNotFound(err) {
		fmt.Println("Creating pod")
		if err := c.Create(ctx, upgrader); err != nil {
			log.Fatal(err)
		}
	} else if err == nil {
		fmt.Println("Pod exists, skipping creation")
	} else {
		return err
	}

waiter:
	for {
		if err := c.Get(ctx, key, pod); apierrors.IsNotFound(err) {
			fmt.Println("Upgrader pod doesn't exist yet, retrying")
			continue
		} else if isConnectionRefusedAPIServer(err) {
			fmt.Println("API server is not up, probably is restarting because of upgrade, retrying")
			time.Sleep(5 * time.Second)
			continue
		} else if err != nil {
			return err
		}

		switch pod.Status.Phase {
		case corev1.PodSucceeded:
			fmt.Printf("Upgrader Pod %s succeed, upgrade process for node %s is done\n", pod.Name, node.Name)
			break waiter
		case corev1.PodFailed:
			fmt.Printf("Upgrader Pod %s has failed: %s\n", pod.Name, pod.Status.Reason)
			return errors.Errorf("upgrader Pod %s has failed: %s", pod.Name, pod.Status.Reason)
		default:
			fmt.Printf("Upgrader Pod has not finished yet (%s)\n", pod.Status.Phase)
		}

		time.Sleep(5 * time.Second)
	}

	if err := c.Delete(ctx, pod, &client.DeleteOptions{}); err != nil {
		return err
	}

	return nil
}

func isConnectionRefusedAPIServer(err error) bool {
	return err != nil && strings.Contains(err.Error(), "connection refused")
}

func upgradeFirstControlPlanePod(nodeName string) *corev1.Pod {
	p := upgradeControlPlanePod(nodeName)
	p.Spec.Containers[0].Args = append(p.Spec.Containers[0].Args, "/usr/local/upgrades/eks-d/upgrade_first_cp.sh", "v1.27.4-eks-1-27-9")

	return p
}

func upgradeRestControlPlanePod(nodeName string) *corev1.Pod {
	p := upgradeControlPlanePod(nodeName)
	p.Spec.Containers[0].Args = append(p.Spec.Containers[0].Args, "/usr/local/upgrades/eks-d/upgrade_rest_cp.sh")

	return p
}

func upgradeControlPlanePod(nodeName string) *corev1.Pod {
	image := "public.ecr.aws/i0f3w2d9/eks-d-in-place-upgrader:v1-27-eks-d-9"
	dirOrCreate := corev1.HostPathDirectoryOrCreate
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("eks-d-upgrader-%s", nodeName),
			Namespace: "eksa-system",
			Labels: map[string]string{
				"ekd-d-upgrader": "true",
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyOnFailure,
			NodeName:      nodeName,
			HostPID:       true,
			Containers: []corev1.Container{
				{
					Name:    "upgrader",
					Image:   "public.ecr.aws/eks-distro-build-tooling/eks-distro-minimal-base-nsenter:latest.2",
					Command: []string{"nsenter"},
					Args: []string{
						"--target",
						"1",
						"--mount",
						"--uts",
						"--ipc",
						"--net",
					},
					SecurityContext: &corev1.SecurityContext{
						Privileged: pointer.Bool(true),
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "root-path",
							MountPath: "/newRoot",
						},
					},
				},
			},
			InitContainers: []corev1.Container{
				{
					Name:    "components-copier",
					Image:   image,
					Command: []string{"cp"},
					Args:    []string{"-r", "/usr/local/eks-d", "/usr/host"},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "host-components",
							MountPath: "/usr/host",
						},
					},
					ImagePullPolicy: corev1.PullAlways,
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "host-components",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/usr/local/upgrades",
							Type: &dirOrCreate,
						},
					},
				},
				{
					Name: "root-path",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/",
						},
					},
				},
			},
		},
	}
}
