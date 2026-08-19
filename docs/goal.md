# App Runner

A way to run applications - either LXC/LXD containers, Docker/Podman containers, or Virtual Machines.

## Tech stack

- backend written in Go, single binary
- frontend written in TypeScript & React with MUI components, managed via Vite
- backend API exposed via Twirp RPC interface
- a CLI (written in Go) to interact with the backend API for similar management/querying as the frontend

## Host requirements

- qemu and `/dev/kvm` access for QEMU/KVM virtual machines
- firecracker command-line tooling for firecracker VMs
- Docker socket access and command-line tooling for Docker containers
- Podman command-line tooling for Podman containers
- access to create network bridges

## Differences to Proxmox

- modern stack
- always uses `host` CPU for virtualisation
- defaults to virtio devices over emulated hardware
