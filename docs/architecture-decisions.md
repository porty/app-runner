# Architectural decisions

This document records the main decisions that define App Runner. It describes the current architecture and its tradeoffs; it is not a deployment or development guide.

## Local, single-operator control plane

**Decision:** App Runner is a control plane for one Linux host and one trusted operator. It listens on loopback by default and does not provide authentication or multi-user isolation.

**Rationale:** The project is intended to make a local virtualisation host coherent, not to reproduce the identity, tenancy, scheduling, and distributed-state concerns of a cloud platform.

**Consequences:** The application must not be treated as a security boundary or exposed directly to an untrusted network. Multi-host orchestration, roles, quotas, and concurrent-administration semantics are outside the current architecture.

## QEMU/KVM is the current compute boundary

**Decision:** The implemented workload type is an x86-64 QEMU virtual machine using KVM acceleration, the host CPU model, Q35, qcow2 storage, and virtio devices.

**Rationale:** A deliberately narrow virtual hardware profile reduces configuration ambiguity and makes lifecycle, storage, console, networking, and boot behavior testable as one system.

**Consequences:** Portability and live migration are not primary goals. Containers, Firecracker, LXC/LXD, and alternative virtual hardware profiles require future design work and should not be implied by the current interface.

## One distributable process

**Decision:** The Go service, RPC API, compiled web interface, and browser-console integration are delivered as one executable. Supporting services owned by App Runner run in-process where practical.

**Rationale:** A single artifact fits the local-host use case and avoids turning the management layer into a suite of independently deployed services.

**Consequences:** The process has a broad set of responsibilities and capability requirements. Internal boundaries therefore matter even though they are not separate deployments, and a process failure can affect every App Runner-owned control-plane service.

## Typed RPC boundary between interface and backend

**Decision:** The browser interface communicates with the backend through a protobuf-defined Twirp API.

**Rationale:** A shared contract keeps UI and backend behavior explicit and leaves room for another client, such as a CLI, without coupling it to frontend internals.

**Consequences:** API changes require coordinated schema and generated-source updates. Generated sources are kept in the repository so building the application does not depend on a protocol-generation toolchain.

## Filesystem-backed local state

**Decision:** VM definitions, disks, leases, configuration, and recovery state are stored as files beneath operator-selected local directories rather than in an external database.

**Rationale:** Local files match the single-host model, keep ownership visible, and make the application independent of another persistent service.

**Consequences:** State is tied to the host filesystem. Concurrent writers, distributed coordination, transactional updates across all state, and database-style querying are not provided. Sensitive VM metadata must rely on local file permissions.

## Direct integration with host facilities

**Decision:** App Runner invokes QEMU directly and uses Linux networking facilities directly. It diagnoses the effective privileges and host configuration instead of hiding them behind a privileged helper or invoking `sudo`.

**Rationale:** The host is intentionally part of the managed system. Direct integration makes the active QEMU processes, bridges, addresses, routes, and firewall rules observable with ordinary host tools.

**Consequences:** The application is Linux-specific in important areas and depends on explicit device access, capabilities, and host tooling. Permission failures are surfaced as operational diagnostics. App Runner must limit privileged mutations to its stated scope and ownership.

## Host networking remains host-owned

**Decision:** Bridge and interface changes made by App Runner affect live kernel state but are not written into NetworkManager, systemd-networkd, Netplan, or distribution-specific configuration.

**Rationale:** Persistently rewriting host networking would require platform-specific policy and could conflict with the system's existing source of truth.

**Consequences:** Confirmed changes may not survive a reboot. The operator remains responsible for persistent host configuration, and App Runner must distinguish its runtime state from configuration owned by other software.

## Risky network changes are confirmable transactions

**Decision:** Before changing bridge or interface topology, App Runner snapshots the affected links, addresses, memberships, and routes. A change is restored unless a local client confirms it within a short window; pending recovery state survives a process restart.

**Rationale:** A network control plane can sever the connection used to operate it. Confirmation and rollback reduce the chance that an incorrect live change leaves the host unreachable.

**Consequences:** Only one such transaction can be pending at a time. Rollback is best-effort against mutable kernel state, so this mechanism reduces risk but is not equivalent to a fully transactional network stack.

## Guest network services follow workload lifecycle

**Decision:** Managed DHCP, DNS, and NAT are configured per bridge and become active only while a managed VM on that bridge needs them. Stable addresses and configuration persist; runtime listeners, forwarding, and firewall changes do not.

**Rationale:** Isolated guest networks should be useful without requiring permanent auxiliary daemons or leaving host-wide forwarding state behind after workloads stop.

**Consequences:** App Runner must reconcile service ownership after restart and coexist with host firewalls and other network services. It manages only its own tagged rules and dedicated state, and it must reject conflicting address ranges or duplicate service ownership.

## Consoles stay behind the control plane

**Decision:** QEMU exposes VNC through a local Unix socket, and App Runner proxies that connection to the embedded browser console.

**Rationale:** Console access is part of VM management and should not require a separately exposed QEMU TCP listener.

**Consequences:** Console availability depends on App Runner, and access inherits the control plane's trust model rather than having an independent authentication boundary.

## Virtual IPMI is an interoperability edge

**Decision:** A VM may expose a persistent virtual BMC on a selected management bridge, translating a constrained IPMI 2.0 command set into App Runner lifecycle and QMP operations.

**Rationale:** IPMI support lets provisioning and management tools control a virtual machine through an interface they already understand, including while the guest is powered off.

**Consequences:** The BMC is intentionally not a complete hardware-management implementation. It adds a network-facing privileged service and is suitable only for a trusted, isolated management network.
