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

components_dir=$(dirname "$(realpath "$0")")

#####################
# kubeadm upgrade
#####################

backup_and_replace /usr/bin/kubeadm "$components_dir" "$components_dir/kubeadm"

kubeadm version
kubeadm upgrade node --ignore-preflight-errors=CoreDNSUnsupportedPlugins,CoreDNSMigration

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
