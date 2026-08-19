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
- the bridge name is configurable and defaults to `br0`
- detects whether bridge networking appears usable by the current process
- logs a backend warning and shows a dismissible frontend warning when bridge networking cannot be configured
- the warning briefly describes likely fixes, including creating the bridge, allowing it in QEMU bridge configuration, and correcting user/group or helper permissions

## Console

- embeds the noVNC browser client in the production frontend
- starts a local Unix-socket VNC console for each running VM
- proxies the noVNC WebSocket connection to that local socket in the Go backend
- does not expose QEMU's VNC listener directly on a network interface

## Completion criteria

- the frontend can create a persistent VM from an ISO already in `iso/`
- the VM can be started, viewed and interacted with through noVNC, gracefully stopped or force stopped, restarted, and deleted
- both NAT and bridge configurations generate the expected QEMU device arguments
- direct navigation to the VM list and console routes works in the production executable
- configuration precedence, persistence, lifecycle behavior, QEMU argument construction, and relevant frontend API behavior have automated tests
