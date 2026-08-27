# App Runner

App Runner is a local control plane for running and managing virtual machines on a Linux host. It brings VM lifecycle, storage, console access, host networking, guest network services, and virtual IPMI into one browser-based interface.

The project is intended for a single operator who wants direct control of QEMU/KVM workloads without adopting a clustered virtualisation platform. It suits development hosts, labs, and small self-contained environments where the host is part of the system being managed and where transparent, local state is preferable to a separate database or collection of services.

App Runner is useful when VM management extends beyond starting a QEMU process. It keeps persistent VM definitions, exposes guest consoles without opening QEMU's VNC service to the network, reports whether the host is correctly prepared for bridge networking, and can manage the runtime network services needed by isolated or routed guest networks. Optional virtual BMC endpoints allow existing IPMI-aware tools to control managed VMs.

Its scope is deliberately smaller than infrastructure platforms such as Proxmox or cloud orchestration systems. App Runner is single-host and single-user, has no authentication boundary of its own, and currently manages x86-64 QEMU/KVM virtual machines only. Containers and other virtual-machine backends remain part of the broader project direction rather than the current implementation.

Some operations modify live host networking and therefore require elevated operating-system capabilities. Those changes are designed for an operator working locally: risky network mutations are temporary until confirmed, and managed DHCP, DNS, and NAT follow the workloads that need them. App Runner does not replace the host's persistent network manager.

The `app-runner` server executable contains both the management service and web interface. The companion `arcli` executable controls that same JSON Twirp API, making it suitable for scripts and terminals. Everything lives on the local filesystem under four directories relative to the working directory: `iso/` (installer ISOs and cloud images), `disk/` (VM disk images), `state/` (VM definitions, DHCP leases, firmware variables, generated cloud-init media, and recovery records), and `run/` (QMP and VNC sockets). Only `iso/` and `disk/` are exposed as browsable storage.

`make build` creates both binaries in `bin/`. By default the CLI connects to `http://127.0.0.1:8080`; use `--server` to target another listener:

```
bin/arcli vm list
bin/arcli --server http://host:8080 storage list
bin/arcli vm start vm-id
bin/arcli net status
```

Run `arcli --help` for its command groups. Successful commands write a table by default; pass `-o json` or `-o yaml` so their results can be piped to other tools. VM operations accept an ID or a name, `arcli vm address --wait 2m NAME` waits for a bridged VM's DHCP lease, and `arcli net leases BRIDGE` lists a bridge's leases. `arcli net bridge configure BRIDGE --dhcp [--nat] [--dns] [--auto-dns]` manages a bridge's guest network services; NAT and DNS stay off unless requested.

A virtual machine has zero or more disks, CD-ROM drives, and network interfaces; nothing is implied. `arcli vm create --name NAME --disk 20 --cdrom installer.iso --nic bridge:br0` creates a conventional installer VM, while `--disk 40:ubuntu.img` creates a linked overlay of a cloud image. Devices are managed afterwards with `arcli vm disk|cdrom|nic`.

Cloud-init support uses `xorriso` to generate per-VM NoCloud ISO images. The binary defaults to `xorriso` and can be changed with the `xorriso_binary` configuration setting or `-xorriso` command-line flag. Reusable user-data templates are stored privately in `state/cloud-init-templates.json`.

QCow2 cloud images can currently be stored in `iso/` with an `.img` or `.qcow2` extension. Disks created from them are linked qcow2 overlays, so the backing image must remain in place; such VMs default to UEFI firmware with a per-VM OVMF variable store. Common OVMF locations are detected automatically; `uefi_code_path` and `uefi_vars_path` override them when needed.

See [Architectural decisions](docs/architecture-decisions.md) for the constraints and tradeoffs that shape the project.
