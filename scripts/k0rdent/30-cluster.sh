#!/usr/bin/env bash
# Deploy a child cluster onto the worker VMs with k0rdent's remote (SSH)
# provider. Mirrors https://docs.k0rdent.io/latest/quickstarts/quickstart-2-remote/
# shellcheck source-path=SCRIPTDIR
# shellcheck source=env.sh
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"
need jq; need ssh; need base64
[[ -f $LAB_KEY ]] || die "run 00-host.sh and 10-vms.sh first"

secret_name=remote-ssh-key
credential_name=remote-cred
resource_template=remote-ssh-key-resource-template

log "Resolving worker addresses"
machines=""
for node in $(worker_names); do
  ip=$(vm_ip "$node")
  log "  $node -> $ip"
  machines+="    - address: $ip
      user: root
      port: 22
"
done

log "Choosing the newest remote-cluster ClusterTemplate"
template=$(mgmt_kubectl -n "$KCM_NS" get clustertemplate -o json |
  jq -r '[.items[] | select(.metadata.name | startswith("remote-cluster-")) | select(.status.valid == true) | .metadata.name] | sort_by(split("-") | map(tonumber? // .)) | last // empty')
[[ -n $template ]] || die "no valid remote-cluster ClusterTemplate in $KCM_NS"
log "  $template"

cat >"$OUT/$CLUSTER_NAME.yaml" <<MANIFESTS
apiVersion: v1
kind: Secret
metadata:
  name: $secret_name
  namespace: $KCM_NS
  labels:
    k0rdent.mirantis.com/component: "kcm"
type: Opaque
data:
  value: $(base64 -w 0 <"$LAB_KEY")
---
apiVersion: k0rdent.mirantis.com/v1beta1
kind: Credential
metadata:
  name: $credential_name
  namespace: $KCM_NS
spec:
  identityRef:
    apiVersion: v1
    kind: Secret
    name: $secret_name
    namespace: $KCM_NS
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: $resource_template
  namespace: $KCM_NS
  labels:
    k0rdent.mirantis.com/component: "kcm"
  annotations:
    projectsveltos.io/template: "true"
---
apiVersion: k0rdent.mirantis.com/v1beta1
kind: ClusterDeployment
metadata:
  name: $CLUSTER_NAME
  namespace: $KCM_NS
spec:
  template: $template
  credential: $credential_name
  propagateCredentials: false
  config:
    controlPlaneNumber: $CONTROL_PLANE_REPLICAS
    k0s:
      version: $K0S_CHILD_VERSION
    k0smotron:
      service:
        type: NodePort
    machines:
$machines
MANIFESTS

log "Applying $OUT/$CLUSTER_NAME.yaml"
# kcm's admission webhooks may still be starting; retry for a while.
for _ in $(seq 1 30); do
  mgmt_kubectl apply -f - <"$OUT/$CLUSTER_NAME.yaml" && break
  sleep 10
done

log "Waiting for ClusterDeployment $CLUSTER_NAME (this takes several minutes)"
# Poll instead of "kubectl wait": watches have been dropped on busy nodes.
deadline=$((SECONDS + 2400))
until [[ $(mgmt_kubectl -n "$KCM_NS" get clusterdeployment "$CLUSTER_NAME" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null) == True ]]; do
  if (( SECONDS >= deadline )); then
    mgmt_kubectl -n "$KCM_NS" get clusterdeployment "$CLUSTER_NAME" -o jsonpath='{.status.conditions}' | jq . || true
    die "timed out waiting for ClusterDeployment $CLUSTER_NAME"
  fi
  sleep 15
done
mgmt_kubectl -n "$KCM_NS" get remotemachines.infrastructure.cluster.x-k8s.io \
  -l "helm.toolkit.fluxcd.io/name=$CLUSTER_NAME" -o custom-columns='NAME:.metadata.name,ADDRESS:.spec.address,READY:.status.ready'

log "Fetching the child kubeconfig to $CHILD_KUBECONFIG"
mgmt_kubectl -n "$KCM_NS" get secret "$CLUSTER_NAME-kubeconfig" -o jsonpath='{.data.value}' | base64 -d >"$CHILD_KUBECONFIG"
chmod 600 "$CHILD_KUBECONFIG"
if command -v kubectl >/dev/null; then
  KUBECONFIG=$CHILD_KUBECONFIG kubectl get nodes -o wide
  KUBECONFIG=$CHILD_KUBECONFIG kubectl get pods -A
fi
log "Child cluster ready. Use: KUBECONFIG=$CHILD_KUBECONFIG kubectl get nodes"
