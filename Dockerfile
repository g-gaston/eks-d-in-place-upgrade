# built from https://github.com/aws/eks-anywhere-build-tooling/pull/2504 and pushed to jgw public ecr
FROM public.ecr.aws/k1e6s8o8/aws/upgrader:v1-27-12-latest
ARG BIN_PATH=/eksa-upgrades/scripts/

COPY upgrade.sh $BIN_PATH/