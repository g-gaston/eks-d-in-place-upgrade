ARG NSENTER=justincormack/nsenter1
FROM $NSENTER as BUILD

FROM alpine:latest
ARG BINARIES="kubectl kubeadm kubelet"
ARG BIN_PATH=/usr/local/eks-d
ARG EKSD_CHANNEL
ARG EKSD_RELEASE
ARG KUBE_VERSION
ARG DOWNLOAD_URL=https://distro.eks.amazonaws.com/kubernetes-$EKSD_CHANNEL/releases/$EKSD_RELEASE/artifacts/kubernetes/$KUBE_VERSION/bin/linux/amd64

COPY --from=BUILD /usr/bin/nsenter1 /usr/bin/nsenter1
RUN mkdir -p $BIN_PATH
RUN for bin in $BINARIES; do wget $DOWNLOAD_URL/$bin -O $BIN_PATH/$bin; chmod +x $BIN_PATH/$bin ; done
COPY upgrade_first_cp.sh $BIN_PATH/
COPY upgrade_rest_cp.sh $BIN_PATH/