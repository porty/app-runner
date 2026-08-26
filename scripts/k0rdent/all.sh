#!/usr/bin/env bash
# Run the whole lab end to end: host -> VMs -> management cluster -> child cluster.
here="$(dirname "${BASH_SOURCE[0]}")"
for step in 00-host 10-vms 20-mgmt 30-cluster; do
  "$here/$step.sh"
done
[[ ${IPMI_ENABLED:-true} == true ]] && "$here/40-ipmi.sh"
