# App Runner

App Runner is a single-user web control plane for local QEMU/KVM virtual machines. It packages the Go/Twirp backend, React management interface, and noVNC console client in one executable.

## Requirements

- Go 1.25 or newer
- Node.js and npm
- Buf, only when regenerating protobuf sources
- `qemu-system-x86_64`, `qemu-img`, and access to `/dev/kvm` to run VMs
- root or `CAP_NET_ADMIN` plus `CAP_NET_BIND_SERVICE` when exposing virtual BMC/IPMI endpoints

## Development

Run the backend and Vite development server together:

```sh
make dev
```

The frontend is available at <http://localhost:5173> with hot module replacement. Vite proxies `/twirp` requests to the Go backend at <http://localhost:8080>.

The two processes can also be run independently:

```sh
go run .
npm --prefix frontend run dev
```

## Test

Run backend tests, frontend tests, and TypeScript checking:

```sh
make test
```

## Production build

Build the frontend and embed it in one statically linked Go executable:

```sh
make build
./bin/app-runner
```

The production application is then available at <http://localhost:8080>. Use `-listen` to select another address, for example `./bin/app-runner -listen 127.0.0.1:9000`.

App Runner currently has no authentication and therefore listens on loopback by default. Do not expose it to an untrusted network.

## Configuration and storage

App Runner reads `app-runner.json` from its current directory when the file exists. Start from [app-runner.example.json](app-runner.example.json) if custom settings are needed. Relative paths are resolved from the current directory.

```json
{
  "listen": "127.0.0.1:8080",
  "iso_dir": "iso",
  "disk_dir": "disk",
  "qemu_binary": "qemu-system-x86_64",
  "qemu_img_binary": "qemu-img"
}
```

Without configuration, `iso/` and `disk/` are created automatically. Place installation images in `iso/`; the UI lists files ending in `.iso`. VM definitions, qcow2 disks, and local QEMU runtime sockets live under `disk/`.

Command-line values override the configuration file:

```sh
./bin/app-runner \
  -config ./app-runner.json \
  -listen 127.0.0.1:9000 \
  -iso-dir ./images \
  -disk-dir ./virtual-machines
```

Use `-qemu` and `-qemu-img` when those binaries are not on the normal executable path.

## VM networking

User-mode NAT works without host network setup. Bridge mode stores a specific bridge name on each VM, so different workloads can use different Linux bridges.

Configuration → Networking provides a live inventory of bridges, member interfaces, addresses, link state, and managed workloads. It also reports the backend's effective username and groups, CAP_NET_ADMIN state, actual `/dev/net/tun` read/write result, and `qemu-bridge-helper` path, ownership, mode, setuid state, file capabilities and per-bridge allow-list result.

Each bridge can optionally run App Runner's embedded DHCPv4 server. Configuration is stored in `disk/dhcp.json`; the default range is `192.168.100.0/24`, with the first usable address assigned to the bridge and dynamic leases beginning at host offset 50 (`192.168.100.50` for the default range). App Runner suggests a different `/24` for each additional bridge and rejects ranges that overlap another managed DHCP range or an existing host-interface subnet.

The DHCP server starts before the first VM using that bridge and stops after the last VM has exited. Leases and stable per-VM MAC addresses are persisted, so clients retain their allocations across App Runner restarts. Without NAT, managed DHCP supplies an address, subnet mask, and broadcast address for a local-only bridge. Do not enable it on a bridge already served by another DHCP server.

NAT can optionally be enabled with managed DHCP. While at least one VM uses the bridge, DHCP advertises the bridge's first usable address (for example `192.168.100.1`) as the router, IPv4 forwarding is enabled, and App Runner installs forwarding and masquerade rules in its dedicated IPv4 nftables table, `app_runner_nat`. When an iptables-nft/UFW `ip filter` table is present, App Runner also inserts tagged, bridge-scoped DHCP, DNS, and forwarding accepts ahead of its policy chains; it removes only those tagged rules and does not flush or replace firewall-owned chains. The rules are rebuilt for all active managed ranges and removed when the last applicable VM stops. App Runner restores the host's previous forwarding setting and keeps `disk/nat-runtime.json` only as crash-recovery ownership state; it does not persist NAT through NetworkManager or another host network manager.

Managed DNS can also be enabled per DHCP bridge. App Runner binds its in-process DNS server to the bridge address on UDP and TCP port 53, advertises that address to DHCP clients, and forwards non-authoritative queries to the configured upstream IP addresses. It starts with the first VM on the bridge and stops with the last. DNS can be used with or without NAT; automatic host-firewall allowances currently follow the NAT lifecycle.

Auto DNS adds an authoritative zone whose suffix defaults to `<bridge>.internal`, advertises that suffix through DHCP, and publishes an A record for each running VM after it acquires a managed DHCP lease. For example, `web-1` on `br0` becomes `web-1.br0.internal`. New VM names must therefore be a single 1–63 character DNS label containing letters, numbers, or interior hyphens. Existing definitions with incompatible names continue to load, but must be recreated with a valid name before Auto DNS can be enabled on their bridge.

Each VM can optionally expose an IPMI 2.0 virtual BMC on a unique IPv4 address attached to a selected management bridge. The listener remains available while the VM is stopped and maps `lanplus` chassis power, reset, shutdown, cycle, status, and supported boot-device commands to App Runner's VM lifecycle and QMP. Managed `/24` bridges suggest addresses below the `.50` DHCP pool; otherwise enter an unused address within a subnet configured on the bridge. IPMI uses UDP port 623, requires `CAP_NET_ADMIN` and `CAP_NET_BIND_SERVICE` (or root), and should be restricted to a trusted management network.

The host can use a managed zone in several ways:

- Query the bridge resolver directly without changing host configuration:

  ```sh
  dig @192.168.100.1 web-1.br0.internal
  ```

- On a systemd-resolved host, route only that suffix to the bridge resolver for the current link lifetime:

  ```sh
  sudo resolvectl dns br0 192.168.100.1
  sudo resolvectl domain br0 '~br0.internal'
  ```

- For persistent host integration, configure the host's existing Unbound, dnsmasq, or CoreDNS instance with a conditional/stub zone pointing at the bridge address. A future App Runner enhancement could instead register and remove per-link routes through systemd-resolved's D-Bus API, or expose one loopback listener that aggregates every active bridge zone.

A typical Linux setup requires:

- an active bridge such as `br0`;
- `allow br0` in `/etc/qemu/bridge.conf`;
- an installed and executable `qemu-bridge-helper` with the permissions configured by the distribution;
- access to `/dev/net/tun`, normally through the device's owning group; and
- membership in the `kvm` group, or equivalent ACL access, for `/dev/kvm`.

App Runner can make runtime-only bridge changes through Linux netlink: create or delete a bridge, bring it up or down, and attach or detach interfaces. Attaching an interface can optionally move its global addresses and non-kernel routes onto the bridge.

Every mutation is snapshotted before it is applied. The frontend must confirm connectivity within 15 seconds or the backend restores the affected link state, bridge membership, addresses and routes. Only one transaction can be pending, rollback state is stored under `disk/`, and an unconfirmed change is restored immediately after an App Runner restart.

Network mutations and managed nftables NAT require root or `CAP_NET_ADMIN`, never invoke `sudo`, and are accepted only from loopback clients. Managed DHCP and DNS additionally need permission to bind privileged UDP/TCP ports 67 and 53 and bind DHCP's socket to a bridge. To grant the capabilities used by bridge, DHCP, DNS, and NAT management to a production build:

```sh
sudo setcap cap_net_admin,cap_net_bind_service,cap_net_raw=+ep ./bin/app-runner
```

Reapply the capability after replacing or rebuilding the executable. QEMU bridge operation separately requires a correctly permissioned `qemu-bridge-helper` and an `allow BRIDGE_NAME` entry in `/etc/qemu/bridge.conf`; the Networking page reports the exact state it observes.

Confirmed changes are not written to NetworkManager, systemd-networkd, Netplan, or distribution configuration and therefore do not survive reboot. Attaching the interface carrying the current connection can still interrupt networking; read the proposed operation carefully and use the rollback countdown.

The optional legacy `bridge_name` configuration or `-bridge` flag is retained only as a migration fallback for VM definitions created before per-VM bridge selection.

## VM lifecycle and console

The UI can create, start, gracefully shut down, force stop, and delete persistent VM definitions. VMs use the Q35 machine, KVM acceleration, the host CPU model, qcow2 disks, virtio devices, and either NAT or bridge networking.

Each running VM exposes VNC only through a local Unix socket. The Go backend proxies the binary connection to the embedded noVNC client at the VM's console route; no QEMU VNC TCP port is opened.

## RPC source generation

Generated Go sources are committed. After changing a protobuf definition, regenerate them with:

```sh
make generate
```

The Twirp generator is pinned as a Go tool dependency, so Buf invokes the repository's declared version.
