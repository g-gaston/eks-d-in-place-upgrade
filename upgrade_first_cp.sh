#!/bin/sh

set -o errexit
set -o nounset
set -x

backup_file() {
  file_path=$1
  backup_folder=$2

  backedup_file="$backup_folder/$(basename "$file_path").bk"

  if test -f "$backedup_file"; then
    return
  fi

  cp "$file_path" "$backedup_file"
}

backup_and_replace() {
  old_f=$1
  backup_folder=$2
  new_f=$3

  backup_file "$old_f" "$backup_folder" && cp "$new_f" "$old_f"
}

kube_version=$1
components_dir=$(dirname "$(realpath "$0")")

#####################
# kubeadm upgrade
#####################

backup_and_replace /usr/bin/kubeadm "$components_dir" "$components_dir/kubeadm"

# Backup and delete coredns configmap. If the CM doesn't exist, kubeadm will skip its upgrade.
# This is desirable for 2 reaons:
# - CAPI already takes care of coredns upgrades
# - kubeadm will fail when verifying the current version of coredns bc the image tag created by
#   eks-s is not recognised by the migration verification logic https://github.com/coredns/corefile-migration/blob/master/migration/versions.go
# Ideally we will instruct kubeadm to just skip coredns upgrade during this phase, but
# it doesn't seem like there is an option.

coredns_backup="${components_dir}/coredns.yaml"
coredns=$(kubectl get cm -n kube-system coredns -oyaml --kubeconfig /etc/kubernetes/admin.conf --ignore-not-found=true)
if [ -n "$coredns" ]; then
  echo "$coredns" >"$coredns_backup"
fi
kubectl delete cm -n kube-system coredns --kubeconfig /etc/kubernetes/admin.conf --ignore-not-found=true

kubeadm version
kubeadm upgrade plan --ignore-preflight-errors=CoreDNSUnsupportedPlugins,CoreDNSMigration
kubeadm upgrade apply "$kube_version" --ignore-preflight-errors=CoreDNSUnsupportedPlugins,CoreDNSMigration --allow-experimental-upgrades --yes

# Restore coredns config backup
kubectl create -f "$coredns_backup" --kubeconfig /etc/kubernetes/admin.conf

###############################
# kubelet and kubectl upgrade
###############################
node=$(hostname)

backup_and_replace /usr/bin/kubectl "$components_dir" "$components_dir/kubectl"
kubectl drain "$node" --ignore-daemonsets --kubeconfig /etc/kubernetes/admin.conf --pod-selector '!ekd-d-upgrader'

systemctl stop kubelet
backup_and_replace /usr/bin/kubelet "$components_dir" "$components_dir/kubelet"
systemctl daemon-reload
systemctl restart kubelet

kubectl uncordon "$node" --kubeconfig /etc/kubernetes/admin.conf

echo "Deleting all leftovers at ${components_dir}"
rm -rf "$components_dir"
