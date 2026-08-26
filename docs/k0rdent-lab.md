# k0rdent lab on App Runner

`scripts/k0rdent/` builds the two k0rdent quickstarts on a single App Runner
host: a [k0s-based management node](https://docs.k0rdent.io/latest/quickstarts/quickstart-1-mgmt-node-and-cluster/)
and a [child cluster deployed over SSH](https://docs.k0rdent.io/latest/quickstarts/quickstart-2-remote/)
onto plain VMs. `arcli` does the VM, network, cloud-init and IPMI work; bash
does the rest over SSH.

```
host ── bridge rofl 192.168.100.0/24 (managed DHCP .50-.254, DNS *.rofl.internal, NAT)
         ├─ k0-mgmt   4 vCPU / 8 GiB / 40 GiB   k0s + kcm (management cluster)   vBMC .20
         ├─ k0-node1  2 vCPU / 3 GiB / 20 GiB   child cluster machine (SSH)      vBMC .21
         └─ k0-node2  2 vCPU / 3 GiB / 20 GiB   child cluster machine (SSH)      vBMC .22
```

Every VM is a linked qcow2 overlay of the Ubuntu Server cloud image with a
NoCloud ISO, so it boots and answers SSH in well under a minute. The child
cluster's control plane runs as k0smotron pods on the management node and is
published as a NodePort; the worker VMs join it over the bridge.

## Prerequisites

- App Runner running with bridge capabilities (`make sudodev` or the `setcap`
  from the Makefile) and `bin/arcli` built.
- `jq`, `ssh`, `ssh-keygen`; optionally `ipmitool` and `kubectl` on the host.
- `/etc/qemu/bridge.conf` allowing the bridge (`allow rofl`).
- Roughly 14 GiB of free memory with the default sizes (8 + 2 x 3 GiB).
- Internet access for the guests: k0s, kcm and images are downloaded inside the
  VMs through the bridge's NAT.

## Steps

Run `scripts/k0rdent/all.sh` or the steps one by one:

1. `00-host.sh` — checks `arcli host status`, creates the bridge if missing
   (`net apply --type create-bridge` then `net confirm` within the 15-second
   window) and enables DHCP/DNS/NAT on it (`net dhcp configure`). Moves the
   cloud image from `disk/` to `iso/` if needed (source images must live under
   `iso_dir`) or downloads it (`storage download`). Generates the lab SSH key in
   `out/k0rdent/`.
2. `10-vms.sh` — writes a cloud-config per node (root SSH keys, inotify/file
   limits), creates each VM from the image (`vm create --image ... --cloud-init-file`),
   attaches a virtual BMC (`vm ipmi`), starts it and waits for its DHCP lease
   (`vm address --wait`) and for SSH.
3. `20-mgmt.sh` — over SSH on `k0-mgmt`: installs k0s as a single untainted
   controller+worker, kubectl, helm, local-path-provisioner as the default
   StorageClass (k0smotron needs persistent volumes for the hosted control
   plane), then `helm install kcm`, trims the `Management` providers to
   k0smotron and sveltos (`KCM_PROVIDERS`) and waits for `Management/kcm` to
   be ready. Copies the kubeconfig to `out/k0rdent/mgmt.kubeconfig`.
4. `30-cluster.sh` — creates the SSH-key `Secret`, `Credential`, resource
   template `ConfigMap` and a `ClusterDeployment` using the newest
   `remote-cluster-*` template with the worker addresses and a single hosted
   control-plane replica (`CONTROL_PLANE_REPLICAS`), waits for it to become
   Ready and saves `out/k0rdent/remote-lab.kubeconfig`.
5. `40-ipmi.sh` — shows power state through the virtual BMCs;
   `40-ipmi.sh k0-node1 power cycle` or `chassis bootdev pxe` drive one node.
6. `90-teardown.sh` — deletes the `ClusterDeployment`, then the VMs and
   `out/k0rdent/`. The bridge stays.

Every script is idempotent and can be re-run after a failure.

## Settings

All values in `scripts/k0rdent/env.sh` are environment overrides, for example:

```
WORKERS=3 NODE_MEMORY=4096 scripts/k0rdent/all.sh
KCM_VERSION=1.11.0 K0S_CHILD_VERSION=v1.35.4+k0s.0 scripts/k0rdent/30-cluster.sh
IMAGE=noble-server-cloudimg-amd64.img \
IMAGE_URL=https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img scripts/k0rdent/all.sh
```

## Reaching the lab

```
ssh -i out/k0rdent/id_ed25519 root@$(bin/arcli -o json vm address k0-mgmt | jq -r '.[0].address')
KUBECONFIG=out/k0rdent/mgmt.kubeconfig kubectl get clusterdeployments -A
KUBECONFIG=out/k0rdent/remote-lab.kubeconfig kubectl get nodes -o wide
bin/arcli net leases rofl
```

Guests resolve each other as `<vm-name>.rofl.internal` through the bridge's DNS.

## Caveats

- k0rdent's remote provider does not list Ubuntu 26.04 (the `resolute` image
  in this repository) as supported yet. It worked here (k0s v1.35.4 on a
  7.0 kernel), but if the child machines fail to provision, switch
  `IMAGE`/`IMAGE_URL` to the 24.04 `noble` image as above.
- Timings on a 16-core host: VMs answer SSH ~10 s after start; k0s plus kcm
  takes 5-10 minutes; the child cluster is Ready about 3 minutes after the
  `ClusterDeployment` is applied.
- The management node needs a default StorageClass and open-file limits; both
  are handled by the scripts but sized for a lab, not production.
- The kcm chart enables every CAPI provider (AWS, Azure, GCP, OpenStack,
  vSphere, Docker, KubeVirt, ...). On a 6 GiB node that starved the API server
  until controllers crash-looped with "leader election lost" and webhooks timed
  out, so `20-mgmt.sh` keeps only the providers the remote template needs and
  the node defaults to 8 GiB. Set `KCM_PROVIDERS=""` to keep the chart
  defaults and raise `MGMT_MEMORY` further.
- Virtual BMC addresses are host addresses on the bridge below the DHCP pool;
  keep `IPMI_BASE` clear of any static leases.
