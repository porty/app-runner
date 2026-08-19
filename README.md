# App Runner

App Runner is a single-user web control plane for local QEMU/KVM virtual machines. It packages the Go/Twirp backend, React management interface, and noVNC console client in one executable.

## Requirements

- Go 1.25 or newer
- Node.js and npm
- Buf, only when regenerating protobuf sources
- `qemu-system-x86_64`, `qemu-img`, and access to `/dev/kvm` to run VMs

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

A typical Linux setup requires:

- an active bridge such as `br0`;
- `allow br0` in `/etc/qemu/bridge.conf`;
- an installed and executable `qemu-bridge-helper` with the permissions configured by the distribution;
- access to `/dev/net/tun`, normally through the device's owning group; and
- membership in the `kvm` group, or equivalent ACL access, for `/dev/kvm`.

App Runner can make runtime-only bridge changes through Linux netlink: create or delete a bridge, bring it up or down, and attach or detach interfaces. Attaching an interface can optionally move its global addresses and non-kernel routes onto the bridge.

Every mutation is snapshotted before it is applied. The frontend must confirm connectivity within 15 seconds or the backend restores the affected link state, bridge membership, addresses and routes. Only one transaction can be pending, rollback state is stored under `disk/`, and an unconfirmed change is restored immediately after an App Runner restart.

Network mutations require root or `CAP_NET_ADMIN`, never invoke `sudo`, and are accepted only from loopback clients. To grant only the bridge-management capability to a production build:

```sh
sudo setcap cap_net_admin=+ep ./bin/app-runner
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
