#!/usr/bin/env bash
# Shared configuration and helpers for the k0rdent lab scripts. Source this
# file; do not run it. Every setting can be overridden from the environment.
# shellcheck disable=SC2034
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# App Runner ------------------------------------------------------------------
: "${ARCLI:=$ROOT/bin/arcli}"                 # arcli binary (make build)
: "${ARCLI_SERVER:=http://127.0.0.1:8080}"    # App Runner listener
: "${ISO_DIR:=$ROOT/iso}"                     # App Runner iso_dir (source images live here)
: "${DISK_DIR:=$ROOT/disk}"                   # App Runner disk_dir

# Guest network: one managed bridge with DHCP, DNS and NAT ---------------------
: "${BRIDGE:=k0rdent}"
: "${CIDR:=192.168.101.0/24}"
: "${DNS_SUFFIX:=k0rdent.internal}"
: "${DNS_FORWARDERS:=1.1.1.1,8.8.8.8}"

# Ubuntu Server cloud image. The lab was written against the 26.04 (resolute)
# image already present in the repository; k0rdent's remote provider does not
# list 26.04 as supported yet, so switch both variables to noble (24.04) if the
# child cluster misbehaves:
#   IMAGE=noble-server-cloudimg-amd64.img
#   IMAGE_URL=https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img
: "${IMAGE:=resolute-server-cloudimg-amd64v3.img}"
: "${IMAGE_URL:=https://cloud-images.ubuntu.com/resolute/current/resolute-server-cloudimg-amd64.img}"

# Virtual machines --------------------------------------------------------------
: "${PREFIX:=k0}"                              # VM names: k0-mgmt, k0-node1, ...
: "${WORKERS:=2}"                              # SSH-managed machines for the child cluster
: "${MGMT_CPUS:=4}" "${MGMT_MEMORY:=8192}" "${MGMT_DISK:=40}"
: "${NODE_CPUS:=2}" "${NODE_MEMORY:=3072}" "${NODE_DISK:=20}"

# Virtual BMCs: one IPMI endpoint per VM on the bridge, outside the DHCP pool.
: "${IPMI_ENABLED:=true}"
: "${IPMI_USER:=admin}" "${IPMI_PASSWORD:=k0rdent}"
: "${IPMI_BASE:=192.168.101.20}"              # k0-mgmt=.20, k0-node1=.21, ...

# k0rdent --------------------------------------------------------------------------
: "${KCM_VERSION:=1.11.0}"                     # helm chart oci://ghcr.io/k0rdent/kcm/charts/kcm
: "${K0S_CHILD_VERSION:=v1.35.4+k0s.0}"        # k0s version for the child cluster
: "${CONTROL_PLANE_REPLICAS:=1}"               # hosted (k0smotron) control plane pods; template default is 3
: "${LOCAL_PATH_PROVISIONER_VERSION:=v0.0.31}" # default StorageClass for k0smotron etcd
: "${CLUSTER_NAME:=remote-lab}"                # ClusterDeployment name
# CAPI providers kept in the Management object. The chart enables every cloud
# provider, which starves a lab-sized node; the remote provider only needs
# k0smotron and sveltos. Set to an empty string to keep the chart defaults.
: "${KCM_PROVIDERS:=cluster-api-provider-k0sproject-k0smotron projectsveltos}"
: "${KCM_NS:=kcm-system}"

# Generated state: lab SSH key, user-data, manifests, kubeconfigs ----------------
: "${OUT:=$ROOT/out/k0rdent}"
LAB_KEY="$OUT/id_ed25519"
MGMT_VM="$PREFIX-mgmt"
MGMT_KUBECONFIG="$OUT/mgmt.kubeconfig"
CHILD_KUBECONFIG="$OUT/$CLUSTER_NAME.kubeconfig"

# Helpers ------------------------------------------------------------------------
log() { printf '\033[1;34m==>\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }
arcli() { "$ARCLI" --server "$ARCLI_SERVER" "$@"; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }

worker_names() {
  local i
  for ((i = 1; i <= WORKERS; i++)); do echo "$PREFIX-node$i"; done
}
all_names() { echo "$MGMT_VM"; worker_names; }

# ipmi_address NAME -> the VM's virtual BMC address (IPMI_BASE + index).
ipmi_address() {
  local name=$1 index=0 candidate
  for candidate in $(all_names); do
    [[ $candidate == "$name" ]] && break
    index=$((index + 1))
  done
  local base=${IPMI_BASE%.*} last=${IPMI_BASE##*.}
  echo "$base.$((last + index))"
}

vm_exists() { arcli -o json vm list | jq -e --arg n "$1" 'any(.[]; .name == $n)' >/dev/null; }
vm_status() { arcli -o json vm get "$1" | jq -r .status; }

# vm_ip NAME [WAIT] -> the guest's DHCP address, waiting for the lease.
vm_ip() { arcli -o json vm address --wait "${2:-180s}" "$1" | jq -r '.[0].address'; }

SSH_OPTS=(-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=5 -o BatchMode=yes)
# ssh_to IP CMD...: run a command as root. ssh joins CMD with spaces for the
# remote shell, so pass a single pre-quoted string when arguments contain
# spaces (see mgmt_kubectl).
ssh_to() { local ip=$1; shift; ssh "${SSH_OPTS[@]}" -i "$LAB_KEY" "root@$ip" "$@"; }
scp_from() { scp "${SSH_OPTS[@]}" -i "$LAB_KEY" "root@$1" "$2"; }

# wait_ssh IP [TIMEOUT_SECONDS]: block until sshd accepts the lab key.
wait_ssh() {
  local ip=$1 timeout=${2:-300} start=$SECONDS
  until ssh_to "$ip" true 2>/dev/null; do
    (( SECONDS - start < timeout )) || die "timed out waiting for SSH on $ip"
    sleep 3
  done
}

# node_ssh NAME CMD...: resolve a VM's address and run a command as root.
node_ssh() { local name=$1; shift; ssh_to "$(vm_ip "$name")" "$@"; }

# mgmt_kubectl ARGS...: kubectl on the management node (stdin is forwarded;
# arguments are quoted so spaces and JSONPath survive the remote shell).
mgmt_kubectl() { ssh_to "$(vm_ip "$MGMT_VM")" "$(printf '%q ' kubectl "$@")"; }
