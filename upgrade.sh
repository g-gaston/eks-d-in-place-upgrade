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

upgrade_components_dir() {
  echo $(dirname "$(realpath "$0")")
}

kubeadm_in_first_cp(){
  kube_version=$1
  etcd_version=$2

  components_dir=$(upgrade_components_dir)

  backup_and_replace /usr/bin/kubeadm "$components_dir" "$components_dir/kubeadm"

  # Backup and delete coredns configmap. If the CM doesn't exist, kubeadm will skip its upgrade.
  # This is desirable for 2 reaons:
  # - CAPI already takes care of coredns upgrades
  # - kubeadm will fail when verifying the current version of coredns bc the image tag created by
  #   eks-s is not recognised by the migration verification logic https://github.com/coredns/corefile-migration/blob/master/migration/versions.go
  # Ideally we will instruct kubeadm to just skip coredns upgrade during this phase, but
  # it doesn't seem like there is an option.

  kubeadm_config_backup="${components_dir}/kubeadm-config.backup.yaml"
  new_kubeadm_config="${components_dir}/kubeadm-config.yaml"
  kubectl get cm -n kube-system kubeadm-config -ojsonpath='{.data.ClusterConfiguration}' --kubeconfig /etc/kubernetes/admin.conf > "$kubeadm_config_backup"
  sed -zE "s/(imageRepository: public.ecr.aws\/eks-distro\/etcd-io\n\s+imageTag: )[^\n]*/\1${etcd_version}/" "$kubeadm_config_backup" > "$new_kubeadm_config"
  #TODO: do the same for the pause image

  coredns_backup="${components_dir}/coredns.yaml"
  coredns=$(kubectl get cm -n kube-system coredns -oyaml --kubeconfig /etc/kubernetes/admin.conf --ignore-not-found=true)
  if [ -n "$coredns" ]; then
    echo "$coredns" >"$coredns_backup"
  fi
  kubectl delete cm -n kube-system coredns --kubeconfig /etc/kubernetes/admin.conf --ignore-not-found=true

  kubeadm version
  kubeadm upgrade plan --ignore-preflight-errors=CoreDNSUnsupportedPlugins,CoreDNSMigration --config "$new_kubeadm_config"
  kubeadm upgrade apply "$kube_version" --config "$new_kubeadm_config" --ignore-preflight-errors=CoreDNSUnsupportedPlugins,CoreDNSMigration --allow-experimental-upgrades --yes

  # Restore coredns config backup
  kubectl create -f "$coredns_backup" --kubeconfig /etc/kubernetes/admin.conf
}

kubeadm_in_rest_cp(){
  components_dir=$(upgrade_components_dir)

  backup_and_replace /usr/bin/kubeadm "$components_dir" "$components_dir/kubeadm"

  kubeadm version
  kubeadm upgrade node --ignore-preflight-errors=CoreDNSUnsupportedPlugins,CoreDNSMigration
}

kubeadm_in_worker() {
  components_dir=$(upgrade_components_dir)

  backup_and_replace /usr/bin/kubeadm "$components_dir" "$components_dir/kubeadm"

  kubeadm version
  kubeadm upgrade node 
}

kubelet_and_kubectl() {
  components_dir=$(upgrade_components_dir)

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

  components_dir=$(upgrade_components_dir)
  echo "Deleting all leftover upgrade components at ${components_dir}"
  rm -rf "$components_dir"
}

$@
