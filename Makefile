REGISTRY ?= public.ecr.aws/i0f3w2d9
UPGRADE_IMAGE_TAG ?= v$(EKSD_CHANNEL)-eks-d-$(EKSD_RELEASE)
UPGRADE_IMAGE_REGISTRY ?= eks-d-in-place-upgrader
UPGRADE_IMAGE_URI ?= $(REGISTRY)/$(UPGRADE_IMAGE_REGISTRY):$(UPGRADE_IMAGE_TAG)

binary:
	go build ./...

image:
	docker build --pull -t $(UPGRADE_IMAGE_URI) .

push: image
	docker push $(UPGRADE_IMAGE_URI)

create-eksd-kind-cluster-1-26:
	kind create cluster --image public.ecr.aws/eks-anywhere/kubernetes-sigs/kind/node:v1.26.7-eks-d-1-26-16-eks-a-47 --config eks-d-1-26-kind.config 
	kind get kubeconfig > kind.kubeconfig

delete-pod-and-run:
	kubectl delete pod -n eksa-system eks-d-upgrader-kind-control-plane || true
	./eks-d-in-place-upgrade kind.kubeconfig
