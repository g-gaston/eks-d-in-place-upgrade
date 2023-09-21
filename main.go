package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/pkg/errors"
	"golang.org/x/exp/slices"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func main() {
	ctx := context.Background()
	c, err := NewRuntimeClientFromFileName(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}

	goClient, err := newGoClient(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}

	if err := setup(ctx, c); err != nil {
		log.Fatal(err)
	}

	nodes, err := getControlPlaneNodes(ctx, c)
	if err != nil {
		log.Fatal(err)
	}

	firstCPNode := nodes[0]
	if err := upgradeFirstControlPlaneNode(ctx, c, goClient, &firstCPNode); err != nil {
		log.Fatal(err)
	}

	for _, node := range nodes[1:] {
		n := node
		if err := upgradeRestControlPlaneNode(ctx, c, goClient, &n); err != nil {
			log.Fatal(err)
		}
	}

	workers, err := getWorkerNodes(ctx, c)
	if err != nil {
		log.Fatal(err)
	}

	for _, node := range workers {
		n := node
		if err := upgradeWorkerNode(ctx, c, goClient, &n); err != nil {
			log.Fatal(err)
		}
	}

	log.Println("🎉 Cluster upgraded 🎉")
}

func setup(ctx context.Context, c client.Client) error {
	objs := []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "eksa-system"}},
		&corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "eks-d-upgrader",
				Namespace: "eksa-system",
			},
		},
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: "drainer",
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get", "list", "delete"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"pods/eviction"},
					Verbs:     []string{"create", "delete"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"nodes"},
					Verbs:     []string{"get", "list", "patch"},
				},
				{
					APIGroups: []string{"apps"},
					Resources: []string{"daemonsets"},
					Verbs:     []string{"get", "list"},
				},
			},
		},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: "eks-d-upgrader-drainer",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Namespace: "eksa-system",
					Name:      "eks-d-upgrader",
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     "drainer",
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
	}

	for _, obj := range objs {
		log.Printf("Creating %s\n", klog.KObj(obj))
		if err := createNoError(ctx, c, obj); err != nil {
			return err
		}
	}

	return nil
}

func createNoError(ctx context.Context, c client.Client, obj client.Object) error {
	if err := c.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

const controlPlaneLabel = "node-role.kubernetes.io/control-plane"

func getControlPlaneNodes(ctx context.Context, c client.Client) ([]corev1.Node, error) {
	return getNodes(ctx, c, client.HasLabels{controlPlaneLabel})
}

func getWorkerNodes(ctx context.Context, c client.Client) ([]corev1.Node, error) {
	selector := labels.NewSelector()
	r, err := labels.NewRequirement(controlPlaneLabel, selection.DoesNotExist, nil)
	if err != nil {
		return nil, err
	}
	selector = selector.Add(*r)

	return getNodes(ctx, c, client.MatchingLabelsSelector{Selector: selector})
}

func getNodes(ctx context.Context, c client.Client, lisOpts ...client.ListOption) ([]corev1.Node, error) {
	nodes := &corev1.NodeList{}

	if err := c.List(ctx, nodes, lisOpts...); err != nil {
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

func upgradeFirstControlPlaneNode(ctx context.Context, c client.Client, logClient *kubernetes.Clientset, node *corev1.Node) error {
	upgrader := upgradeFirstControlPlanePod(node.Name)
	return upgradeNode(ctx, c, logClient, node, upgrader)
}

func upgradeRestControlPlaneNode(ctx context.Context, c client.Client, logClient *kubernetes.Clientset, node *corev1.Node) error {
	upgrader := upgradeRestControlPlanePod(node.Name)
	return upgradeNode(ctx, c, logClient, node, upgrader)
}

func upgradeWorkerNode(ctx context.Context, c client.Client, logClient *kubernetes.Clientset, node *corev1.Node) error {
	upgrader := upgradeWorkerPod(node.Name)
	return upgradeNode(ctx, c, logClient, node, upgrader)
}

const targetVersion = "v1.27.4-eks-cedffd4"

func upgradeNode(ctx context.Context, c client.Client, logClient *kubernetes.Clientset, node *corev1.Node, upgrader *corev1.Pod) error {
	log.Printf("Upgrading node %s\n", node.Name)
	key := client.ObjectKeyFromObject(upgrader)
	pod := &corev1.Pod{}

	if err := c.Get(ctx, key, pod); apierrors.IsNotFound(err) {
		if node.Status.NodeInfo.KubeletVersion == targetVersion {
			log.Printf("Node %s was already in desired version\n", node.Name)
			return nil
		}

		log.Println("Creating pod")
		if err := c.Create(ctx, upgrader); err != nil {
			log.Fatal(err)
		}
	} else if err == nil {
		log.Println("Pod exists, skipping creation")
	} else {
		return err
	}

	if err := getPodLogs(ctx, logClient, upgrader); err != nil {
		return err
	}

waiter:
	for {
		if err := c.Get(ctx, key, pod); apierrors.IsNotFound(err) {
			log.Println("Upgrader pod doesn't exist yet, retrying")
			continue
		} else if isConnectionRefusedAPIServer(err) {
			log.Println("API server is not up, probably is restarting because of upgrade, retrying")
			time.Sleep(5 * time.Second)
			continue
		} else if err != nil {
			return err
		}

		switch pod.Status.Phase {
		case corev1.PodSucceeded:
			log.Printf("Upgrader Pod %s succeed, upgrade process for node %s is done\n", pod.Name, node.Name)
			break waiter
		case corev1.PodFailed:
			log.Printf("Upgrader Pod %s has failed: %s\n", pod.Name, pod.Status.Reason)
			return errors.Errorf("upgrader Pod %s has failed: %s", pod.Name, pod.Status.Reason)
		default:
			log.Printf("Upgrader Pod has not finished yet (%s)\n", pod.Status.Phase)
		}

		time.Sleep(5 * time.Second)
	}

	log.Printf("Deleting upgrader pod %s after success\n", upgrader.Name)
	if err := c.Delete(ctx, pod, &client.DeleteOptions{}); err != nil {
		return err
	}

	return nil
}

func isConnectionRefusedAPIServer(err error) bool {
	return err != nil && strings.Contains(err.Error(), "connection refused")
}

func isConnectionResetAPIServer(err error) bool {
	return err != nil && strings.Contains(err.Error(), "connection reset by peer")
}

func isPodInitializing(err error) bool {
	return err != nil && strings.Contains(err.Error(), "is waiting to start: PodInitializing")
}

const (
	image         = "public.ecr.aws/i0f3w2d9/eks-d-in-place-upgrader:v1-27-eks-d-9"
	upgradeScript = "/usr/local/upgrades/eks-d/upgrade.sh"
)

func upgradeFirstControlPlanePod(nodeName string) *corev1.Pod {
	p := upgradePod(nodeName)
	p.Spec.InitContainers = containersForUpgrade(image, nodeName, "kubeadm_in_first_cp", "v1.27.4-eks-1-27-9")
	p.Spec.Containers = []corev1.Container{printAndCleanupContainer()}

	return p
}

func upgradeRestControlPlanePod(nodeName string) *corev1.Pod {
	p := upgradePod(nodeName)
	p.Spec.InitContainers = containersForUpgrade(image, nodeName, "kubeadm_in_rest_cp")
	p.Spec.Containers = []corev1.Container{printAndCleanupContainer()}

	return p
}

func upgradeWorkerPod(nodeName string) *corev1.Pod {
	p := upgradePod(nodeName)
	p.Spec.InitContainers = containersForUpgrade(image, nodeName, "kubeadm_in_worker")
	p.Spec.Containers = []corev1.Container{printAndCleanupContainer()}

	return p
}

func containersForUpgrade(image, nodeName string, kubeadmUpgradeCommand ...string) []corev1.Container {
	return []corev1.Container{
		copierContainer(image),
		nsenterContainer("kubeadm-upgrader", append([]string{upgradeScript}, kubeadmUpgradeCommand...)...),
		drainerContainer(image, nodeName),
		nsenterContainer("kubelet-kubelet-upgrader", upgradeScript, "kubelet_and_kubectl"),
		uncordonContainer(image, nodeName),
	}
}

func printAndCleanupContainer() corev1.Container {
	return nsenterContainer("post-upgrade-status", upgradeScript, "print_status_and_cleanup")
}

func upgradePod(nodeName string) *corev1.Pod {
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
			ServiceAccountName: "eks-d-upgrader",
			RestartPolicy:      corev1.RestartPolicyOnFailure,
			NodeName:           nodeName,
			HostPID:            true,
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
			},
		},
	}
}

func copierContainer(image string) corev1.Container {
	return corev1.Container{
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
	}
}

func nsenterContainer(containerName string, command ...string) corev1.Container {
	c := baseNsenterContainer()
	c.Name = containerName
	c.Args = append(c.Args, command...)
	return c
}

func baseNsenterContainer() corev1.Container {
	return corev1.Container{
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
			Privileged: ptr.To(true),
		},
	}
}

func drainerContainer(image, nodeName string) corev1.Container {
	return corev1.Container{
		Name:            "drain",
		Image:           image,
		Command:         []string{"/usr/local/eks-d/kubectl"},
		Args:            []string{"drain", nodeName, "--ignore-daemonsets", "--pod-selector", "!ekd-d-upgrader"},
		ImagePullPolicy: corev1.PullAlways,
	}
}

func uncordonContainer(image, nodeName string) corev1.Container {
	return corev1.Container{
		Name:            "uncordon",
		Image:           image,
		Command:         []string{"/usr/local/eks-d/kubectl"},
		Args:            []string{"uncordon", nodeName},
		ImagePullPolicy: corev1.PullAlways,
	}
}

func getPodLogs(ctx context.Context, clientSet *kubernetes.Clientset, pod *corev1.Pod) error {
	blue := color.New(color.FgBlue).SprintfFunc()
	yellow := color.New(color.FgYellow).SprintfFunc()
	podName := pod.Name
	namespace := pod.Namespace

	color.Yellow("_____________________________________________________________________")
	log.Println(yellow("%s upgrade process start", pod.Spec.NodeName))
	color.Yellow("_____________________________________________________________________")

containers:
	for _, container := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
		podLogOpts := corev1.PodLogOptions{}
		podLogOpts.Follow = true
		podLogOpts.TailLines = ptr.To(int64(100))
		podLogOpts.Container = container.Name
		var podLogs io.ReadCloser
		var err error
		log.Println(yellow("Phase %s in %s", container.Name, pod.Spec.NodeName))
		log.Printf("Waiting for %s to start\n", container.Name)
		for {
			podLogs, err = clientSet.CoreV1().Pods(namespace).GetLogs(podName, &podLogOpts).Stream(ctx)
			if apierrors.IsNotFound(err) || isPodInitializing(err) || isConnectionRefusedAPIServer(err) || isConnectionResetAPIServer(err) {
				continue
			}
			if err != nil {
				return err
			}
			break
		}

		color.Yellow("------------------------------------------------------")
		reader := bufio.NewScanner(podLogs)
		for reader.Scan() {
			select {
			case <-ctx.Done():
				podLogs.Close()
				continue containers
			default:
				line := reader.Text()
				fmt.Printf("%s %s\n", blue("[%s]", podLogOpts.Container), line)
			}
		}
		if reader.Err() != nil {
			log.Println("INFO log EOF " + reader.Err().Error() + ": " + podLogOpts.Container)
		}
		podLogs.Close()
		color.Yellow("------------------------------------------------------")
	}

	return nil
}
