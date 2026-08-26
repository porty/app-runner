package apprunner

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
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
		"unix:" + settings.vncSocketPath("vm-id"),
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

func TestQEMUArgumentsMountCloudInitISO(t *testing.T) {
	settings := defaultConfig(t.TempDir())
	qemu := newQEMUHypervisor(settings)
	arguments := qemu.arguments(virtualMachine{ID: "vm-id", NetworkMode: networkModeNAT, CloudInit: vmCloudInit{Enabled: true}})

	for _, expected := range []string{
		"file=" + settings.cloudInitISOPath("vm-id") + ",if=none,id=cloudinit,media=cdrom,readonly=on",
		"scsi-cd,drive=cloudinit,bus=scsi0.0,id=cloudinitdev",
	} {
		if !slices.Contains(arguments, expected) {
			t.Fatalf("cloud-init argument %q is missing: %#v", expected, arguments)
		}
	}
}

func TestQEMUCreatesLinkedQCow2Disk(t *testing.T) {
	qemuImg, err := exec.LookPath("qemu-img")
	if err != nil {
		t.Skip("qemu-img is not installed")
	}
	directory := t.TempDir()
	backingPath := filepath.Join(directory, "ubuntu.img")
	if output, err := exec.Command(qemuImg, "create", "-f", "qcow2", backingPath, "4G").CombinedOutput(); err != nil {
		t.Fatalf("create backing image: %s: %v", output, err)
	}
	settings := defaultConfig(directory)
	settings.QEMUImgBinary = qemuImg
	qemu := newQEMUHypervisor(settings)
	overlayPath := filepath.Join(directory, "overlay.qcow2")
	if err := qemu.CreateLinkedDisk(context.Background(), overlayPath, backingPath, 20); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(qemuImg, "info", "--output=json", overlayPath).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect overlay: %s: %v", output, err)
	}
	var info struct {
		Format          string `json:"format"`
		VirtualSize     uint64 `json:"virtual-size"`
		BackingFilename string `json:"backing-filename"`
	}
	if err := json.Unmarshal(output, &info); err != nil {
		t.Fatal(err)
	}
	if info.Format != "qcow2" || info.VirtualSize != 20<<30 || info.BackingFilename != backingPath {
		t.Fatalf("unexpected linked overlay information: %#v", info)
	}
}

func TestQEMUArgumentsUsePersistentUEFIFirmware(t *testing.T) {
	settings := defaultConfig(t.TempDir())
	settings.UEFICodePath = "/firmware/OVMF_CODE.fd"
	qemu := newQEMUHypervisor(settings)
	arguments := qemu.arguments(virtualMachine{ID: "vm-id", Firmware: vmFirmwareUEFI, NetworkMode: networkModeNAT})
	for _, expected := range []string{
		"if=pflash,format=raw,unit=0,readonly=on,file=/firmware/OVMF_CODE.fd",
		"if=pflash,format=raw,unit=1,file=" + settings.uefiVarsPath("vm-id"),
	} {
		if !slices.Contains(arguments, expected) {
			t.Fatalf("UEFI argument %q is missing: %#v", expected, arguments)
		}
	}
}

func TestQEMUArgumentsHonorIPMIBootDevice(t *testing.T) {
	qemu := newQEMUHypervisor(defaultConfig(t.TempDir()))
	arguments := qemu.arguments(virtualMachine{ID: "vm-id", ISOName: "installer.iso", IPMI: vmIPMIConfig{BootDevice: uint8(1)}})
	if !slices.Contains(arguments, "order=ncd,menu=on") {
		t.Fatalf("PXE boot order is missing: %#v", arguments)
	}
}
