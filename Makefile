REGISTRY ?= public.ecr.aws/i0f3w2d9
EKSD_CHANNEL ?= 1-27
EKSD_RELEASE ?= 12
KUBE_VERSION ?= v1.27.5
UPGRADE_IMAGE_TAG ?= v$(EKSD_CHANNEL)-eks-d-$(EKSD_RELEASE)
UPGRADE_IMAGE_REGISTRY ?= eks-d-in-place-upgrader
UPGRADE_IMAGE_URI ?= $(REGISTRY)/$(UPGRADE_IMAGE_REGISTRY):$(UPGRADE_IMAGE_TAG)
KIND_CLUSTER_NAME ?= eksd-1-26-upgrader-test

KUBECTL ?= bin/kubectl
KIND ?= bin/kind

binary:
	go build github.com/g-gaston/eks-d-in-place-upgrade

image:
	docker build --pull -t $(UPGRADE_IMAGE_URI) .

push: image
	docker push $(UPGRADE_IMAGE_URI)

create-eksd-kind-cluster-1-26: $(KIND)
	$(KIND) create cluster --name $(KIND_CLUSTER_NAME) --image public.ecr.aws/eks-anywhere/kubernetes-sigs/kind/node:v1.26.7-eks-d-1-26-16-eks-a-47 --config eks-d-1-26-kind.config || true
	$(KIND) get kubeconfig --name $(KIND_CLUSTER_NAME) > kind.kubeconfig

delete-eksd-kind-cluster-1-26: $(KIND)
	$(KIND) delete cluster --name $(KIND_CLUSTER_NAME)
	rm kind.kubeconfig

delete-pod: $(KUBECTL)
	$(KUBECTL) delete pod -n eksa-system eks-d-upgrader-$(KIND_CLUSTER_NAME)-control-plane || true

delete-pod-and-run: delete-pod run-for-kind

run-for-kind: binary
	./eks-d-in-place-upgrade kind.kubeconfig ${UPGRADE_IMAGE_URI}

test-in-kind: create-eksd-kind-cluster-1-26 run-for-kind delete-eksd-kind-cluster-1-26

push-and-test-in-kind: push test-in-kind

bin:
	mkdir -p bin

$(KUBECTL): bin
	curl -Lo $(KUBECTL) https://distro.eks.amazonaws.com/kubernetes-$(EKSD_CHANNEL)/releases/$(EKSD_RELEASE)/artifacts/kubernetes/$(KUBE_VERSION)/bin/linux/amd64/kubectl
	chmod +x $(KUBECTL)

$(KIND): bin
	curl -Lo $(KIND) https://kind.sigs.k8s.io/dl/v0.20.0/kind-linux-amd64
	chmod +x $(KIND)