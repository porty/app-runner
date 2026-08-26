#!/usr/bin/env bash
# Create and boot the management node and the SSH-managed worker machines from
# the Ubuntu cloud image, with cloud-init granting root SSH access.
# shellcheck source-path=SCRIPTDIR
# shellcheck source=env.sh
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"
need jq; need ssh
[[ -f $LAB_KEY ]] || die "run 00-host.sh first"

# user_data NAME: cloud-config for a node. The hostname comes from App Runner's
# meta-data (local-hostname = VM name), so it is not repeated here.
user_data() {  # NAME is accepted for symmetry; the hostname comes from meta-data.
  cat <<CLOUD
#cloud-config
disable_root: false
ssh_pwauth: false
users:
  - name: root
    ssh_authorized_keys:
      - $(cat "$LAB_KEY.pub")$( [[ -f $HOME/.ssh/id_ed25519.pub ]] && printf '\n      - %s' "$(cat "$HOME/.ssh/id_ed25519.pub")" )
write_files:
  - path: /etc/sysctl.d/99-k0rdent.conf
    content: |
      fs.inotify.max_user_instances = 8192
      fs.inotify.max_user_watches = 1048576
      fs.file-max = 2097152
runcmd:
  - sysctl --system
CLOUD
}

create_vm() {
  local name=$1 cpus=$2 memory=$3 disk=$4
  if vm_exists "$name"; then
    log "$name already exists"
  else
    user_data "$name" >"$OUT/$name-user-data.yaml"
    log "Creating $name ($cpus vCPU, ${memory} MiB, ${disk} GiB) from $IMAGE"
    arcli vm create --name "$name" --cpus "$cpus" --memory "$memory" --disk "$disk" \
      --image "$IMAGE" --network bridge --bridge "$BRIDGE" \
      --cloud-init-file "$OUT/$name-user-data.yaml" >/dev/null
  fi
  if [[ $IPMI_ENABLED == true ]]; then
    arcli vm ipmi "$name" --enabled --bridge "$BRIDGE" --address "$(ipmi_address "$name")" \
      --username "$IPMI_USER" --password "$IPMI_PASSWORD" >/dev/null
  fi
  if [[ $(vm_status "$name") != VM_STATUS_RUNNING ]]; then
    log "Starting $name"
    arcli vm start "$name" >/dev/null
  fi
}

create_vm "$MGMT_VM" "$MGMT_CPUS" "$MGMT_MEMORY" "$MGMT_DISK"
for node in $(worker_names); do
  create_vm "$node" "$NODE_CPUS" "$NODE_MEMORY" "$NODE_DISK"
done

for name in $(all_names); do
  ip=$(vm_ip "$name")
  log "$name is at $ip; waiting for SSH"
  wait_ssh "$ip"
  ssh_to "$ip" cloud-init status --wait >/dev/null || true
done

log "Nodes:"
arcli net leases "$BRIDGE"
