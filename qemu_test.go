package main

import (
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"testing"
)

func TestQEMUArgumentsUseRequiredVirtualisationProfile(t *testing.T) {
	settings := defaultConfig(t.TempDir())
	qemu := newQEMUHypervisor(settings)
	vm := virtualMachine{
		ID: "vm-id", Name: "test VM", CPUs: 4, MemoryMiB: 4096,
		ISOName: "installer.iso", NetworkMode: networkModeNAT, MACAddress: "52:54:00:11:22:33",
	}
	arguments := qemu.arguments(vm)

	for _, expected := range []string{
		"q35,accel=kvm",
		"host",
		"virtio-vga",
		"virtio-blk-pci,drive=system",
		"virtio-scsi-pci,id=scsi0",
		"scsi-cd,drive=install,bus=scsi0.0,id=cdromdev0",
		"user,id=net0",
		"virtio-net-pci,netdev=net0,mac=52:54:00:11:22:33",
		"unix:" + filepath.Join(settings.DiskDir, "vm-id.vnc.sock"),
	} {
		if !slices.Contains(arguments, expected) {
			t.Errorf("QEMU arguments do not contain %q: %#v", expected, arguments)
		}
	}
}

func TestExecuteQMPNegotiatesAndSendsCommand(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "qmp.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverResult := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverResult <- acceptErr
			return
		}
		defer connection.Close()
		decoder := json.NewDecoder(connection)
		encoder := json.NewEncoder(connection)
		if encodeErr := encoder.Encode(map[string]any{"QMP": map[string]any{"version": map[string]any{}}}); encodeErr != nil {
			serverResult <- encodeErr
			return
		}
		for _, expected := range []string{"qmp_capabilities", "system_powerdown"} {
			var request map[string]string
			if decodeErr := decoder.Decode(&request); decodeErr != nil {
				serverResult <- decodeErr
				return
			}
			if request["execute"] != expected {
				serverResult <- fmt.Errorf("expected %s, got %s", expected, request["execute"])
				return
			}
			if encodeErr := encoder.Encode(map[string]any{"return": map[string]any{}}); encodeErr != nil {
				serverResult <- encodeErr
				return
			}
		}
		serverResult <- nil
	}()

	if err := executeQMP(socketPath, "system_powerdown"); err != nil {
		t.Fatalf("executeQMP returned an error: %v", err)
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
}

func TestQEMUArgumentsSupportBridgeNetworking(t *testing.T) {
	settings := defaultConfig(t.TempDir())
	settings.BridgeName = "lab0"
	qemu := newQEMUHypervisor(settings)
	arguments := qemu.arguments(virtualMachine{ID: "vm-id", ISOName: "installer.iso", NetworkMode: networkModeBridge, BridgeName: "lab0"})

	if !slices.Contains(arguments, "bridge,id=net0,br=lab0") {
		t.Fatalf("bridge network arguments are missing: %#v", arguments)
	}
	if slices.Contains(arguments, "user,id=net0") {
		t.Fatalf("NAT arguments were included for a bridge VM: %#v", arguments)
	}
}

func TestQEMUArgumentsHonorIPMIBootDevice(t *testing.T) {
	qemu := newQEMUHypervisor(defaultConfig(t.TempDir()))
	arguments := qemu.arguments(virtualMachine{ID: "vm-id", ISOName: "installer.iso", IPMI: vmIPMIConfig{BootDevice: uint8(1)}})
	if !slices.Contains(arguments, "order=ncd,menu=on") {
		t.Fatalf("PXE boot order is missing: %#v", arguments)
	}
}
