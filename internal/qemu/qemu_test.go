package qemu

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/roflware/app-runner/internal/config"
	"github.com/roflware/app-runner/internal/serialconsole"
	"github.com/roflware/app-runner/internal/vm"
)

func newTestHypervisor(settings config.Config) *Hypervisor {
	return New(settings, serialconsole.New(settings.SerialSocketPath))
}

func TestQEMUArgumentsUseRequiredVirtualisationProfile(t *testing.T) {
	settings := config.Default(t.TempDir())
	qemu := newTestHypervisor(settings)
	machine := vm.VirtualMachine{
		ID: "machine-id", Name: "test VM", CPUs: 4, MemoryMiB: 4096,
		Disks:  []vm.Disk{{ID: "d0", SizeGiB: 20}},
		CDROMs: []vm.CDROM{{ID: "c0", ISOName: "installer.iso"}},
		NICs:   []vm.NIC{{ID: "n0", Mode: vm.NetworkModeNAT, MACAddress: "52:54:00:11:22:33"}},
	}
	arguments := qemu.arguments(machine)

	for _, expected := range []string{
		"q35,accel=kvm",
		"host",
		"virtio-vga",
		"file=" + settings.VMDiskPath("machine-id", "d0") + ",if=none,id=disk0,format=qcow2,cache=none",
		"virtio-blk-pci,drive=disk0",
		"virtio-scsi-pci,id=scsi0",
		"scsi-cd,drive=install,bus=scsi0.0,id=cdromdev0",
		"user,id=net0",
		"virtio-net-pci,netdev=net0,mac=52:54:00:11:22:33",
		"unix:" + settings.VNCSocketPath("machine-id"),
		"socket,id=serial0,path=" + settings.SerialSocketPath("machine-id") + ",server=on,wait=on",
		"chardev:serial0",
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

func TestQEMUArgumentsSupportMultipleNetworkInterfaces(t *testing.T) {
	settings := config.Default(t.TempDir())
	qemu := newTestHypervisor(settings)
	arguments := qemu.arguments(vm.VirtualMachine{ID: "machine-id", NICs: []vm.NIC{
		{ID: "n0", Mode: vm.NetworkModeBridge, BridgeName: "lab0", MACAddress: "52:54:00:00:00:01"},
		{ID: "n1", Mode: vm.NetworkModeNAT, MACAddress: "52:54:00:00:00:02"},
	}})

	for _, expected := range []string{
		"bridge,id=net0,br=lab0", "virtio-net-pci,netdev=net0,mac=52:54:00:00:00:01",
		"user,id=net1", "virtio-net-pci,netdev=net1,mac=52:54:00:00:00:02",
	} {
		if !slices.Contains(arguments, expected) {
			t.Fatalf("network argument %q is missing: %#v", expected, arguments)
		}
	}
}

func TestQEMUArgumentsOmitDevicesTheVMDoesNotHave(t *testing.T) {
	settings := config.Default(t.TempDir())
	arguments := newTestHypervisor(settings).arguments(vm.VirtualMachine{ID: "machine-id", Name: "bare"})
	for _, argument := range arguments {
		if strings.HasPrefix(argument, "file=") || strings.HasPrefix(argument, "virtio-net") || strings.HasPrefix(argument, "user,") || strings.HasPrefix(argument, "bridge,") {
			t.Fatalf("device-less VM received device argument %q: %#v", argument, arguments)
		}
	}
}

func TestQEMUArgumentsMountCloudInitISO(t *testing.T) {
	settings := config.Default(t.TempDir())
	qemu := newTestHypervisor(settings)
	arguments := qemu.arguments(vm.VirtualMachine{ID: "machine-id", CloudInit: vm.CloudInit{Enabled: true}})

	for _, expected := range []string{
		"file=" + settings.CloudInitISOPath("machine-id") + ",if=none,id=cloudinit,media=cdrom,readonly=on",
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
	settings := config.Default(directory)
	settings.QEMUImgBinary = qemuImg
	qemu := newTestHypervisor(settings)
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
	settings := config.Default(t.TempDir())
	settings.UEFICodePath = "/firmware/OVMF_CODE.fd"
	qemu := newTestHypervisor(settings)
	arguments := qemu.arguments(vm.VirtualMachine{ID: "machine-id", Firmware: vm.FirmwareUEFI})
	for _, expected := range []string{
		"if=pflash,format=raw,unit=0,readonly=on,file=/firmware/OVMF_CODE.fd",
		"if=pflash,format=raw,unit=1,file=" + settings.VMUEFIVarsPath("machine-id"),
	} {
		if !slices.Contains(arguments, expected) {
			t.Fatalf("UEFI argument %q is missing: %#v", expected, arguments)
		}
	}
}

func TestQEMUArgumentsHonorIPMIBootDevice(t *testing.T) {
	settings := config.Default(t.TempDir())
	qemu := newTestHypervisor(settings)
	arguments := qemu.arguments(vm.VirtualMachine{ID: "machine-id", IPMI: vm.IPMIConfig{BootDevice: uint8(1)}})
	if !slices.Contains(arguments, "order=ncd,menu=on") {
		t.Fatalf("PXE boot order is missing: %#v", arguments)
	}
}

func TestIsRunningRequiresTheVMsOwnQEMUProcess(t *testing.T) {
	settings := config.Default(t.TempDir())
	qemu := newTestHypervisor(settings)
	socketPath := settings.QMPSocketPath("machine-id")
	// A process that merely exists (this test binary) is not the VM.
	if qemu.IsRunning(vm.VirtualMachine{ID: "machine-id", PID: os.Getpid()}) {
		t.Fatal("an unrelated live PID was treated as the VM's QEMU process")
	}
	// A process whose command line references the VM's QMP socket is.
	// A loop body stops the shell from exec-ing sleep directly, which would
	// drop the marker argument from the visible command line.
	command := exec.Command("sh", "-c", "while :; do sleep 1; done", "sh", "unix:"+socketPath+",server=on,wait=off")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _, _ = command.Process.Wait() })
	// Start returns before the kernel has finished publishing the new argv,
	// so give /proc a moment to reflect the exec.
	recognised := false
	for attempt := 0; attempt < 100 && !recognised; attempt++ {
		recognised = qemu.IsRunning(vm.VirtualMachine{ID: "machine-id", PID: command.Process.Pid})
		if !recognised {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !recognised {
		t.Fatal("the VM's own process was not recognised as running")
	}
	if qemu.IsRunning(vm.VirtualMachine{ID: "other-machine", PID: command.Process.Pid}) {
		t.Fatal("a process serving another VM's socket was treated as this VM")
	}
	if qemu.IsRunning(vm.VirtualMachine{ID: "machine-id", PID: 0}) {
		t.Fatal("PID 0 was treated as running")
	}
}
