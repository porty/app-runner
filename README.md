# App Runner

App Runner is a local control plane for running and managing virtual machines on a Linux host. It brings VM lifecycle, storage, console access, host networking, guest network services, and virtual IPMI into one browser-based interface.

The project is intended for a single operator who wants direct control of QEMU/KVM workloads without adopting a clustered virtualisation platform. It suits development hosts, labs, and small self-contained environments where the host is part of the system being managed and where transparent, local state is preferable to a separate database or collection of services.

App Runner is useful when VM management extends beyond starting a QEMU process. It keeps persistent VM definitions, exposes guest consoles without opening QEMU's VNC service to the network, reports whether the host is correctly prepared for bridge networking, and can manage the runtime network services needed by isolated or routed guest networks. Optional virtual BMC endpoints allow existing IPMI-aware tools to control managed VMs.

Its scope is deliberately smaller than infrastructure platforms such as Proxmox or cloud orchestration systems. App Runner is single-host and single-user, has no authentication boundary of its own, and currently manages x86-64 QEMU/KVM virtual machines only. Containers and other virtual-machine backends remain part of the broader project direction rather than the current implementation.

Some operations modify live host networking and therefore require elevated operating-system capabilities. Those changes are designed for an operator working locally: risky network mutations are temporary until confirmed, and managed DHCP, DNS, and NAT follow the workloads that need them. App Runner does not replace the host's persistent network manager.

The project is distributed as one executable containing both the management service and web interface. Its state remains on the local filesystem, alongside the VM disks and runtime data it manages.

See [Architectural decisions](docs/architecture-decisions.md) for the constraints and tradeoffs that shape the project.
