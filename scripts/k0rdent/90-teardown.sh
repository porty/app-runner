#!/usr/bin/env bash
# Remove the child cluster, then every lab VM and generated state. The bridge
# and its DHCP configuration are left in place.
# shellcheck source-path=SCRIPTDIR
# shellcheck source=env.sh
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"
need jq

if vm_exists "$MGMT_VM" && [[ $(vm_status "$MGMT_VM") == VM_STATUS_RUNNING ]] && [[ -f $LAB_KEY ]]; then
  if mgmt_kubectl -n "$KCM_NS" get clusterdeployment "$CLUSTER_NAME" >/dev/null 2>&1; then
    log "Deleting ClusterDeployment $CLUSTER_NAME"
    mgmt_kubectl -n "$KCM_NS" delete clusterdeployment "$CLUSTER_NAME" --wait --timeout=20m || true
  fi
fi

for name in $(all_names); do
  vm_exists "$name" || continue
  if [[ $(vm_status "$name") != VM_STATUS_STOPPED ]]; then
    log "Stopping $name"
    arcli vm stop "$name" --force >/dev/null
  fi
  log "Deleting $name"
  arcli vm delete "$name" >/dev/null
done

if [[ -d $OUT ]]; then
  log "Removing $OUT"
  rm -rf "$OUT"
fi
log "Lab removed"
