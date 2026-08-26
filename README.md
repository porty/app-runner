# App Runner

App Runner is a local control plane for running and managing virtual machines on a Linux host. It brings VM lifecycle, storage, console access, host networking, guest network services, and virtual IPMI into one browser-based interface.

The project is intended for a single operator who wants direct control of QEMU/KVM workloads without adopting a clustered virtualisation platform. It suits development hosts, labs, and small self-contained environments where the host is part of the system being managed and where transparent, local state is preferable to a separate database or collection of services.

App Runner is useful when VM management extends beyond starting a QEMU process. It keeps persistent VM definitions, exposes guest consoles without opening QEMU's VNC service to the network, reports whether the host is correctly prepared for bridge networking, and can manage the runtime network services needed by isolated or routed guest networks. Optional virtual BMC endpoints allow existing IPMI-aware tools to control managed VMs.

Its scope is deliberately smaller than infrastructure platforms such as Proxmox or cloud orchestration systems. App Runner is single-host and single-user, has no authentication boundary of its own, and currently manages x86-64 QEMU/KVM virtual machines only. Containers and other virtual-machine backends remain part of the broader project direction rather than the current implementation.

Some operations modify live host networking and therefore require elevated operating-system capabilities. Those changes are designed for an operator working locally: risky network mutations are temporary until confirmed, and managed DHCP, DNS, and NAT follow the workloads that need them. App Runner does not replace the host's persistent network manager.

The `app-runner` server executable contains both the management service and web interface. The companion `arcli` executable controls that same JSON Twirp API, making it suitable for scripts and terminals. State remains on the local filesystem, alongside the VM disks and runtime data it manages.

`make build` creates both binaries in `bin/`. By default the CLI connects to `http://127.0.0.1:8080`; use `--server` to target another listener:

```
bin/arcli vm list
bin/arcli --server http://host:8080 storage list
bin/arcli vm start vm-id
bin/arcli net status
```

Run `arcli --help` for its command groups. Successful commands write a table by default; pass `-o json` or `-o yaml` so their results can be piped to other tools.

Cloud-init support uses `xorriso` to generate per-VM NoCloud ISO images. The binary defaults to `xorriso` and can be changed with the `xorriso_binary` configuration setting or `-xorriso` command-line flag. Reusable user-data templates are stored privately in `disk/cloud-init-templates.json`.

QCow2 cloud images can currently be stored in `iso/` with an `.img` or `.qcow2` extension. VMs created from them use a linked qcow2 overlay and per-VM OVMF variable store, so the backing image must remain in place. Common OVMF locations are detected automatically; `uefi_code_path` and `uefi_vars_path` override them when needed.

See [Architectural decisions](docs/architecture-decisions.md) for the constraints and tradeoffs that shape the project.
