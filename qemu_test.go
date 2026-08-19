package main

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestQEMUArgumentsUseRequiredVirtualisationProfile(t *testing.T) {
	settings := defaultConfig(t.TempDir())
	qemu := newQEMUHypervisor(settings)
	vm := virtualMachine{
		ID: "vm-id", Name: "test VM", CPUs: 4, MemoryMiB: 4096,
		ISOName: "installer.iso", NetworkMode: networkModeNAT,
	}
	arguments := qemu.arguments(vm)

	for _, expected := range []string{
		"q35,accel=kvm",
		"host",
		"virtio-vga",
		"virtio-blk-pci,drive=system",
		"virtio-scsi-pci,id=scsi0",
		"scsi-cd,drive=install,bus=scsi0.0",
		"user,id=net0",
		"virtio-net-pci,netdev=net0",
		"unix:" + filepath.Join(settings.DiskDir, "vm-id.vnc.sock"),
	} {
		if !slices.Contains(arguments, expected) {
			t.Errorf("QEMU arguments do not contain %q: %#v", expected, arguments)
		}
	}
}

func TestQEMUArgumentsSupportBridgeNetworking(t *testing.T) {
	settings := defaultConfig(t.TempDir())
	settings.BridgeName = "lab0"
	qemu := newQEMUHypervisor(settings)
	arguments := qemu.arguments(virtualMachine{ID: "vm-id", ISOName: "installer.iso", NetworkMode: networkModeBridge})

	if !slices.Contains(arguments, "bridge,id=net0,br=lab0") {
		t.Fatalf("bridge network arguments are missing: %#v", arguments)
	}
	if slices.Contains(arguments, "user,id=net0") {
		t.Fatalf("NAT arguments were included for a bridge VM: %#v", arguments)
	}
}
