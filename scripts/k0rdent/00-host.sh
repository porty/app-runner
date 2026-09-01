#!/usr/bin/env bash
# Prepare the App Runner host: bridge with DHCP/DNS/NAT, cloud image, lab key.
# shellcheck source-path=SCRIPTDIR
# shellcheck source=env.sh
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"
need jq; need ssh-keygen
[[ -x $ARCLI ]] || die "$ARCLI not found; run make build"
mkdir -p "$OUT"

log "Checking App Runner at $ARCLI_SERVER"
arcli host status >/dev/null || die "App Runner is not reachable"
arcli -o json host status | jq -e '.qemu_available and .kvm_available' >/dev/null || die "QEMU/KVM is unavailable on this host"

if ! arcli -o json net status | jq -e --arg b "$BRIDGE" 'any(.bridges[]; .name == $b)' >/dev/null; then
  log "Creating bridge $BRIDGE (changes are temporary until confirmed)"
  change=$(arcli -o json net apply --type create-bridge --bridge "$BRIDGE" | jq -r .id)
  arcli net confirm "$change" >/dev/null
fi
bridge_json=$(arcli -o json net status | jq --arg b "$BRIDGE" '.bridges[] | select(.name == $b)')
if ! jq -e '.dhcp.enabled == true' <<<"$bridge_json" >/dev/null; then
  log "Enabling managed DHCP, DNS and NAT on $BRIDGE ($CIDR)"
  arcli net bridge configure "$BRIDGE" --dhcp --cidr "$CIDR" --nat --dns --auto-dns --dns-forwarders "$DNS_FORWARDERS" \
    --dns-suffix "$DNS_SUFFIX" --description "k0rdent lab" >/dev/null
fi
jq -e '.usable_by_qemu == true' <<<"$bridge_json" >/dev/null || {
  jq -r '.diagnostics[] | select(.status != "DIAGNOSTIC_STATUS_PASS") | "  \(.label): \(.detail) \(.remediation)"' <<<"$bridge_json" >&2
  die "bridge $BRIDGE is not usable by QEMU; fix the diagnostics above"
}

# Source images must live under iso_dir; a copy dropped into disk_dir is moved.
if [[ -f $DISK_DIR/$IMAGE && ! -e $ISO_DIR/$IMAGE ]]; then
  log "Moving $IMAGE from $DISK_DIR to $ISO_DIR"
  mv "$DISK_DIR/$IMAGE" "$ISO_DIR/$IMAGE"
fi
if ! arcli -o json image list | jq -e --arg i "$IMAGE" 'any(.[]; .name == $i)' >/dev/null; then
  log "Downloading $IMAGE from $IMAGE_URL"
  arcli --timeout 30m storage download --root iso --url "$IMAGE_URL" --filename "$IMAGE" >/dev/null
fi

if [[ ! -f $LAB_KEY ]]; then
  log "Generating lab SSH key $LAB_KEY"
  ssh-keygen -q -t ed25519 -N '' -C "k0rdent-lab" -f "$LAB_KEY"
fi
log "Host ready: bridge=$BRIDGE image=$IMAGE key=$LAB_KEY"
