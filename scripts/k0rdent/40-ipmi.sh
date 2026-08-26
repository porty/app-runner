#!/usr/bin/env bash
# Talk to the virtual BMCs App Runner exposes for the lab VMs.
#   40-ipmi.sh                 power status of every node
#   40-ipmi.sh <name> ARGS...  run ipmitool against one node, e.g.
#                              40-ipmi.sh k0-node1 power cycle
#                              40-ipmi.sh k0-node1 chassis bootdev pxe
# shellcheck source-path=SCRIPTDIR
# shellcheck source=env.sh
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"
need ipmitool
[[ $IPMI_ENABLED == true ]] || die "IPMI_ENABLED is not true"

ipmi() { local name=$1; shift; ipmitool -I lanplus -H "$(ipmi_address "$name")" -U "$IPMI_USER" -P "$IPMI_PASSWORD" "$@"; }

if (( $# == 0 )); then
  for name in $(all_names); do
    printf '%-12s %-16s %s\n' "$name" "$(ipmi_address "$name")" "$(ipmi "$name" power status 2>&1)"
  done
else
  name=$1; shift
  ipmi "$name" "$@"
fi
