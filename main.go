package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func main() {
	// nodeName := "w-cluster-2-cmtrh"
	nodeName := "w-cluster-2-t56rb"
	ctx := context.Background()
	c, err := NewRuntimeClientFromFileName(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}

	upgraderPod := upgradeRestControlPlanePod(nodeName)
	key := client.ObjectKeyFromObject(upgraderPod)
	existingPod := &corev1.Pod{}
	err = c.Get(ctx, key, existingPod)
	if err == nil {
		fmt.Println("Pod exists, deleting")
		if err := c.Delete(ctx, existingPod, &client.DeleteOptions{}); err != nil {
			log.Fatal(err)
		}
		time.Sleep(2 * time.Second)
	} else if !apierrors.IsNotFound(err) {
		log.Fatal(err)
	}

	fmt.Println("Creating pod")
	if err := c.Create(ctx, upgraderPod); err != nil {
		log.Fatal(err)
	}
}

// NewRuntimeClientFromFileName creates a new controller runtime client given a kubeconfig filename.
func NewRuntimeClientFromFileName(kubeConfigFilename string) (client.Client, error) {
	data, err := os.ReadFile(kubeConfigFilename)
	if err != nil {
		return nil, fmt.Errorf("failed to create new client: %s", err)
	}

	return newRuntimeClient(data, nil, runtime.NewScheme())
}

func initScheme(scheme *runtime.Scheme) error {
	adders := append([]schemeAdder{
		clientgoscheme.AddToScheme,
	}, schemeAdders...)
	if scheme == nil {
		return fmt.Errorf("scheme was not provided")
	}
	return addToScheme(scheme, adders...)
}

func newRuntimeClient(data []byte, rc restConfigurator, scheme *runtime.Scheme) (client.Client, error) {
	if rc == nil {
		rc = restConfigurator(clientcmd.RESTConfigFromKubeConfig)
	}
	restConfig, err := rc.Config(data)
	if err != nil {
		return nil, err
	}

	if err := initScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to init client scheme %v", err)
	}

	err = clientgoscheme.AddToScheme(scheme)
	if err != nil {
		return nil, err
	}

	return client.New(restConfig, client.Options{Scheme: scheme})
}

// restConfigurator abstracts the creation of a controller-runtime *rest.Config.
//
// This abstraction improves testing, as all known methods of instantiating a
// *rest.Config try to make network calls, and that's something we'd like to
// keep out of our unit tests as much as possible. In addition, where we do
// use them in unit tests, we need to be prepared with a controller-runtime
// EnvTest environment.
//
// For normal, non-test use, this can safely be ignored.
type restConfigurator func([]byte) (*rest.Config, error)

// Config generates and returns a rest.Config from a kubeconfig in bytes.
func (c restConfigurator) Config(data []byte) (*rest.Config, error) {
	return c(data)
}

type schemeAdder func(s *runtime.Scheme) error

var schemeAdders = []schemeAdder{
	// clientgoscheme adds all the native K8s kinds
	clientgoscheme.AddToScheme,
}

func addToScheme(scheme *runtime.Scheme, schemeAdders ...schemeAdder) error {
	for _, adder := range schemeAdders {
		if err := adder(scheme); err != nil {
			return err
		}
	}

	return nil
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
