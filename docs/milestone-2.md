# Milestone 2

Launch and manage QEMU/KVM virtual machines from the web interface.

## Scope

- virtual machines only; containers and Firecracker remain out of scope
- no authentication or management CLI yet
- single-user operation from the directory where App Runner is launched
- persistent VM definitions that survive App Runner restarts
- create, list, inspect, start, gracefully stop, force stop, and delete virtual machines
- reconcile the recorded state of running VMs when App Runner starts

## Virtual machine profile

- x86-64 QEMU/KVM using the Q35 machine type
- always uses KVM acceleration and `host` CPU
- configurable name, vCPU count, memory, system disk size, installation ISO, and network mode
- qcow2 system disks
- virtio system disk and network devices
- installation media attached through a virtio SCSI controller

## Storage and configuration

- reads an optional `app-runner.json` configuration file from the current directory
- command-line flags override values loaded from the configuration file
- defaults ISO storage to `iso/` and VM data storage to `disk/`, relative to the current directory
- creates both directories when they do not exist
- lists existing ISO files for selection in the frontend; uploading ISOs is out of scope
- stores VM disks, runtime sockets, and persistent VM metadata beneath `disk/`

## Networking

- supports user-mode NAT and host bridge networking
- stores the selected bridge on each bridged VM and supports multiple host bridges
- provides a Networking page that lists Linux bridges, member interfaces, addresses, operational state, and assigned managed workloads
- distinguishes managed VMs and containers from host interfaces that cannot be attributed to an App Runner workload
- reports the backend username, UID, groups, CAP_NET_ADMIN state, actual `/dev/net/tun` read/write access, `qemu-bridge-helper` ownership and permissions, file capabilities, and bridge allow-list results
- provides specific remediation for each failed diagnostic
- can create and delete runtime Linux bridges, bring them up or down, and attach or detach host interfaces
- can migrate global addresses and non-kernel routes when attaching an interface
- snapshots affected link state, bridge membership, addresses, and routes before a mutation
- allows one pending network mutation at a time and automatically restores its snapshot unless confirmed from the frontend within 15 seconds
- persists pending rollback data and restores it immediately if App Runner restarts before confirmation
- accepts network mutation RPCs only from loopback clients and requires root or CAP_NET_ADMIN
- does not invoke `sudo` and does not write persistent NetworkManager, systemd-networkd, Netplan, or distribution network configuration
- optionally configures an embedded, in-process DHCPv4 server for each bridge using a persisted IPv4 CIDR
- assigns the first usable address to a DHCP-enabled bridge and begins dynamic leases at host offset 50, leaving lower addresses available for static assignments
- suggests distinct private `/24` ranges for multiple bridges and rejects overlapping managed or host-interface subnets
- starts a bridge's DHCP server before its first VM starts and stops it after the last VM on that bridge exits
- persists DHCP leases and stable VM MAC addresses across backend restarts
- reports DHCP socket capability diagnostics; DHCP requires UDP port 67 and bind-to-interface access in addition to bridge-management permission
- optionally enables runtime-only NAT for a managed DHCP bridge, advertising the bridge address as the router and managing IPv4 forwarding plus isolated nftables forwarding/masquerade rules with the same first-VM/last-VM lifecycle
- restores the host's prior forwarding setting and removes its dedicated nftables table after the last applicable VM stops or during abandoned-state recovery; it does not persist these settings through a host network manager
- optionally runs an in-process forwarding DNS server on a DHCP bridge over UDP and TCP, advertises the bridge address as DNS, and follows the same first-VM/last-VM lifecycle
- optionally publishes authoritative A records from active VM DHCP leases under a configurable Auto DNS suffix, while validating new VM names as DNS hostname labels in both the backend and frontend

## Console

- embeds the noVNC browser client in the production frontend
- starts a local Unix-socket VNC console for each running VM
- proxies the noVNC WebSocket connection to that local socket in the Go backend
- does not expose QEMU's VNC listener directly on a network interface

## Completion criteria

- the frontend can create a persistent VM from an ISO already in `iso/`
- the VM can be started, viewed and interacted with through noVNC, gracefully stopped or force stopped, restarted, and deleted
- both NAT and bridge configurations generate the expected QEMU device arguments
- networking inventory and diagnostics identify bridge usability and managed workload assignments
- unconfirmed network mutations are reverted after 15 seconds and after a backend restart
- DHCP configuration, `.50` pool allocation, lease persistence, packet responses, and per-bridge VM lifecycle are covered by automated tests
- direct navigation to the VM list and console routes works in the production executable
- configuration precedence, persistence, lifecycle behavior, QEMU argument construction, and relevant frontend API behavior have automated tests
