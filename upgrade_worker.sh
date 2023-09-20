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

kubeadm_upgrade() {
  components_dir=$(dirname "$(realpath "$0")")

  backup_and_replace /usr/bin/kubeadm "$components_dir" "$components_dir/kubeadm"

  kubeadm version
  kubeadm upgrade node 
}

kubelet_kubectl_upgrade() {
  components_dir=$(dirname "$(realpath "$0")")

  backup_and_replace /usr/bin/kubectl "$components_dir" "$components_dir/kubectl"

  systemctl stop kubelet
  backup_and_replace /usr/bin/kubelet "$components_dir" "$components_dir/kubelet"
  systemctl daemon-reload
  systemctl restart kubelet
}

print_status() {
  systemctl status containerd
  systemctl status kubelet
  kubeadm version
}

print_status_and_cleanup() {
  print_status

  components_dir=$(dirname "$(realpath "$0")")
  echo "Deleting all leftovers at ${components_dir}"
  rm -rf "$components_dir"
}

$1
