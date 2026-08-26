#!/usr/bin/env bash
# Turn the management VM into a k0rdent management cluster:
# k0s (single node, untainted) + kubectl + helm + default StorageClass + kcm.
# Mirrors https://docs.k0rdent.io/latest/quickstarts/quickstart-1-mgmt-node-and-cluster/
# shellcheck source-path=SCRIPTDIR
# shellcheck source=env.sh
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"
need jq; need ssh
mgmt_ip=$(vm_ip "$MGMT_VM")
log "Management node $MGMT_VM at $mgmt_ip"

# Arguments are quoted for the remote shell so KCM_PROVIDERS stays one word.
ssh_to "$mgmt_ip" "$(printf '%q ' bash -s -- "$KCM_VERSION" "$LOCAL_PATH_PROVISIONER_VERSION" "$KCM_NS" "$KCM_PROVIDERS")" <<'REMOTE'
set -euo pipefail
kcm_version=$1 lpp_version=$2 ns=$3 providers=$4
export DEBIAN_FRONTEND=noninteractive

if ! command -v k0s >/dev/null; then
  echo "==> Installing k0s"
  curl --proto '=https' --tlsv1.2 -sSf https://get.k0s.sh | sh
fi
if [ ! -f /etc/systemd/system/k0scontroller.service ]; then
  echo "==> Installing the k0s controller with an untainted worker"
  k0s install controller --enable-worker --no-taints
fi
k0s start 2>/dev/null || true
echo "==> Waiting for the k0s API"
until k0s kubectl get nodes >/dev/null 2>&1; do sleep 3; done

command -v jq >/dev/null || { apt-get update -qq && apt-get install -y -qq jq >/dev/null; }
if ! command -v kubectl >/dev/null; then
  echo "==> Installing kubectl"
  release=$(curl -fsSL https://dl.k8s.io/release/stable.txt)
  curl -fsSL "https://dl.k8s.io/release/$release/bin/linux/amd64/kubectl" -o /usr/local/bin/kubectl
  chmod 755 /usr/local/bin/kubectl
fi
mkdir -p /root/.kube
k0s kubeconfig admin >/root/.kube/config
chmod 600 /root/.kube/config
echo "==> Waiting for the node to register and become Ready"
until [ "$(kubectl get nodes --no-headers 2>/dev/null | wc -l)" -ge 1 ]; do sleep 3; done
kubectl wait --for=condition=Ready node --all --timeout=10m

if ! command -v helm >/dev/null; then
  echo "==> Installing helm"
  curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
fi

echo "==> Installing local-path-provisioner as the default StorageClass"
kubectl apply -f "https://raw.githubusercontent.com/rancher/local-path-provisioner/$lpp_version/deploy/local-path-storage.yaml"
kubectl patch storageclass local-path -p '{"metadata":{"annotations":{"storageclass.kubernetes.io/is-default-class":"true"}}}'

if [ "$(helm list -n "$ns" -o json 2>/dev/null | jq -r '.[] | select(.name == "kcm" and .status == "deployed") | .chart')" = "kcm-$kcm_version" ]; then
  echo "==> kcm $kcm_version is already installed"
else
  echo "==> Installing kcm $kcm_version"
  # Admission webhooks can be briefly unreachable right after a reboot; retry.
  for attempt in 1 2 3 4 5; do
    helm upgrade --install kcm oci://ghcr.io/k0rdent/kcm/charts/kcm --version "$kcm_version" -n "$ns" --create-namespace --wait --timeout 20m && break
    [ "$attempt" -lt 5 ] || exit 1
    sleep 30
  done
fi

echo "==> Waiting for the Management object"
until kubectl -n "$ns" get management kcm >/dev/null 2>&1; do sleep 5; done
if [ -n "$providers" ]; then
  echo "==> Limiting Management providers to: $providers"
  patch=$(printf '%s\n' $providers | jq -R . | jq -sc '{spec: {providers: map({name: .})}}')
  # The validating webhook may still be starting; retry for a while.
  for _ in $(seq 1 30); do
    kubectl -n "$ns" patch management kcm --type merge -p "$patch" && break
    sleep 10
  done
fi
echo "==> Waiting for the Management object to become ready"
# Poll the Ready condition instead of "kubectl wait": watches have been
# dropped on busy nodes, and kcm 1.11 has no status.ready field.
deadline=$((SECONDS + 1800))
until [ "$(kubectl -n "$ns" get management kcm -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)" = True ]; do
  if [ "$SECONDS" -ge "$deadline" ]; then
    kubectl -n "$ns" get management kcm -o jsonpath='{.status.conditions}' | jq . || true
    echo "timed out waiting for the Management object" >&2
    exit 1
  fi
  sleep 10
done
kubectl get pods -n "$ns"
kubectl get pods -n projectsveltos
kubectl get providertemplate -n "$ns"
kubectl get clustertemplate -n "$ns"
REMOTE

log "Copying the management kubeconfig to $MGMT_KUBECONFIG"
ssh_to "$mgmt_ip" k0s kubeconfig admin >"$MGMT_KUBECONFIG"
chmod 600 "$MGMT_KUBECONFIG"
log "k0rdent is ready. Use: KUBECONFIG=$MGMT_KUBECONFIG kubectl get management -n $KCM_NS"
