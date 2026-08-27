package apprunner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type memoryVMStore struct {
	vms []virtualMachine
}

type fakeVMNetworkLifecycle struct {
	prepared []string
	released []string
	err      error
}

func (l *fakeVMNetworkLifecycle) Prepare(vm virtualMachine) error {
	l.prepared = append(l.prepared, vm.ID)
	return l.err
}

func (l *fakeVMNetworkLifecycle) Release(vm virtualMachine) {
	l.released = append(l.released, vm.ID)
}

func (s *memoryVMStore) Load() ([]virtualMachine, error) {
	return slices.Clone(s.vms), nil
}

func (s *memoryVMStore) Save(vms []virtualMachine) error {
	s.vms = slices.Clone(vms)
	return nil
}

type fakeHypervisor struct {
	running      map[int]bool
	nextPID      int
	gracefulStop bool
	forcedStop   bool
	reset        bool
	onExit       func(int, error)
	changedCDROM string
	changedISO   string
	linkedSource string
}

func newFakeHypervisor() *fakeHypervisor {
	return &fakeHypervisor{running: map[int]bool{}, nextPID: 1200}
}

func (h *fakeHypervisor) CreateDisk(_ context.Context, path string, _ uint32) error {
	return os.WriteFile(path, []byte("qcow2"), 0o600)
}

func (h *fakeHypervisor) CreateLinkedDisk(_ context.Context, path, backingPath string, _ uint32) error {
	h.linkedSource = backingPath
	return os.WriteFile(path, []byte("linked qcow2"), 0o600)
}

func (h *fakeHypervisor) Start(_ virtualMachine, onExit func(int, error)) (int, error) {
	h.nextPID++
	h.running[h.nextPID] = true
	h.onExit = onExit
	return h.nextPID, nil
}

func (h *fakeHypervisor) IsRunning(vm virtualMachine) bool {
	return h.running[vm.PID]
}

func (h *fakeHypervisor) GracefulStop(vm virtualMachine) error {
	h.gracefulStop = true
	return nil
}

func (h *fakeHypervisor) ForceStop(vm virtualMachine) error {
	h.forcedStop = true
	delete(h.running, vm.PID)
	if h.onExit != nil {
		// QEMU's reaper reports the exit asynchronously, as the real hypervisor does.
		go h.onExit(vm.PID, errors.New("signal: killed"))
	}
	return nil
}

func (h *fakeHypervisor) Reset(virtualMachine) error {
	h.reset = true
	return nil
}

func (h *fakeHypervisor) ChangeCDROMMedia(_ virtualMachine, cdromID, isoPath string) error {
	h.changedCDROM = cdromID
	h.changedISO = isoPath
	return nil
}

func testCapabilities() hostCapabilities {
	return hostCapabilities{QEMUAvailable: true, KVMAvailable: true}
}

func newTestVMManager(t *testing.T) (*vmManager, *fakeHypervisor, config) {
	t.Helper()
	workingDirectory := t.TempDir()
	settings := defaultConfig(workingDirectory)
	if err := prepareDirectories(settings); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settings.ISODir, "installer.iso"), []byte("iso"), 0o600); err != nil {
		t.Fatal(err)
	}
	hypervisor := newFakeHypervisor()
	manager, err := newVMManager(vmManagerOptions{
		Settings: settings, Store: &memoryVMStore{}, Hypervisor: hypervisor, Capabilities: testCapabilities,
		BridgeCapability: func(string) (bool, string) { return true, "" },
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.newID = func() (string, error) { return "vm-id", nil }
	manager.now = func() time.Time { return time.Date(2026, 8, 19, 1, 2, 3, 0, time.UTC) }
	return manager, hypervisor, settings
}

// installerVMOptions describes a conventional VM: one disk, one installer
// CD-ROM, and one NAT interface.
func installerVMOptions(name string) createVMOptions {
	return createVMOptions{
		Name: name, CPUs: 2, MemoryMiB: 2048,
		Disks:  []vmDiskSpec{{SizeGiB: 20}},
		CDROMs: []vmCDROMSpec{{ISOName: "installer.iso"}},
		NICs:   []vmNICSpec{{Mode: networkModeNAT}},
	}
}

func bridgedVMOptions(name, bridge string) createVMOptions {
	options := installerVMOptions(name)
	options.NICs = []vmNICSpec{{Mode: networkModeBridge, BridgeName: bridge}}
	return options
}

func TestVMManagerLifecycle(t *testing.T) {
	manager, hypervisor, _ := newTestVMManager(t)
	vm, err := manager.Create(context.Background(), installerVMOptions("Development"))
	if err != nil {
		t.Fatalf("Create returned an error: %v", err)
	}
	if vm.Status != vmStatusStopped || vm.ID != "vm-id" || vm.Firmware != vmFirmwareBIOS {
		t.Fatalf("unexpected created VM: %#v", vm)
	}
	if len(vm.Disks) != 1 || len(vm.CDROMs) != 1 || len(vm.NICs) != 1 || vm.NICs[0].MACAddress == "" {
		t.Fatalf("created VM does not expose its devices: %#v", vm)
	}
	if _, err := os.Stat(manager.vmDiskPath(vm.ID, vm.Disks[0])); err != nil {
		t.Fatalf("disk was not created: %v", err)
	}

	vm, err = manager.Start(vm.ID)
	if err != nil {
		t.Fatalf("Start returned an error: %v", err)
	}
	if vm.Status != vmStatusRunning || vm.PID == 0 {
		t.Fatalf("VM was not marked running: %#v", vm)
	}

	vm, err = manager.Stop(vm.ID, false, 0)
	if err != nil {
		t.Fatalf("graceful Stop returned an error: %v", err)
	}
	if !hypervisor.gracefulStop || vm.Status != vmStatusStopping {
		t.Fatalf("graceful stop was not requested: %#v", vm)
	}
	hypervisor.running[vm.PID] = false
	hypervisor.onExit(vm.PID, nil)

	vm, err = manager.Get(vm.ID)
	if err != nil || vm.Status != vmStatusStopped {
		t.Fatalf("VM did not transition to stopped: %#v, %v", vm, err)
	}
	if err := manager.Delete(vm.ID); err != nil {
		t.Fatalf("Delete returned an error: %v", err)
	}
	if _, err := manager.Get(vm.ID); !errors.Is(err, errVMNotFound) {
		t.Fatalf("deleted VM is still present: %v", err)
	}
}

func TestVMManagerAllowsVMsWithoutDevices(t *testing.T) {
	manager, _, _ := newTestVMManager(t)
	vm, err := manager.Create(context.Background(), createVMOptions{Name: "bare", CPUs: 1, MemoryMiB: 512})
	if err != nil {
		t.Fatalf("Create returned an error: %v", err)
	}
	if len(vm.Disks) != 0 || len(vm.CDROMs) != 0 || len(vm.NICs) != 0 {
		t.Fatalf("device-less VM gained implied devices: %#v", vm)
	}
	if _, err := manager.Start(vm.ID); err != nil {
		t.Fatalf("device-less VM could not start: %v", err)
	}
	if _, err := manager.Stop(vm.ID, true, 0); err != nil {
		t.Fatal(err)
	}
	if err := manager.Delete(vm.ID); err != nil {
		t.Fatal(err)
	}
}

func TestVMManagerCreatesCloudImageVMAsLinkedUEFIDisk(t *testing.T) {
	manager, hypervisor, settings := newTestVMManager(t)
	cloudImageDirectory := filepath.Join(settings.ISODir, "cloud")
	if err := os.MkdirAll(cloudImageDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	cloudImagePath := filepath.Join(cloudImageDirectory, "ubuntu.img")
	if err := os.WriteFile(cloudImagePath, []byte("backing qcow2"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager.settings.UEFICodePath = filepath.Join(settings.StateDir, "OVMF_CODE.fd")
	manager.settings.UEFIVarsPath = filepath.Join(settings.StateDir, "OVMF_VARS.fd")
	if err := os.WriteFile(manager.settings.UEFICodePath, []byte("code"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.settings.UEFIVarsPath, []byte("vars"), 0o600); err != nil {
		t.Fatal(err)
	}

	vm, err := manager.Create(context.Background(), createVMOptions{
		Name: "ubuntu-cloud", CPUs: 2, MemoryMiB: 2048,
		Disks:     []vmDiskSpec{{SizeGiB: 20, SourceImage: "cloud/ubuntu.img"}},
		NICs:      []vmNICSpec{{Mode: networkModeNAT}},
		CloudInit: vmCloudInit{Enabled: true, UserData: "#cloud-config\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if hypervisor.linkedSource != cloudImagePath {
		t.Fatalf("linked backing path = %q, want %q", hypervisor.linkedSource, cloudImagePath)
	}
	if vm.Disks[0].SourceImage != "cloud/ubuntu.img" || vm.Firmware != vmFirmwareUEFI || len(vm.CDROMs) != 0 {
		t.Fatalf("unexpected cloud-image VM: %#v", vm)
	}
	contents, err := os.ReadFile(manager.uefiVarsPath(vm.ID))
	if err != nil || string(contents) != "vars" {
		t.Fatalf("UEFI variable store = %q, %v", contents, err)
	}
	if _, err := manager.Start(vm.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Stop(vm.ID, true, 0); err != nil {
		t.Fatal(err)
	}
	if err := manager.Delete(vm.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(manager.uefiVarsPath(vm.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted VM retained UEFI variables: %v", err)
	}
}

func TestVMManagerListsNestedCloudDiskImages(t *testing.T) {
	manager, _, settings := newTestVMManager(t)
	directory := filepath.Join(settings.ISODir, "ubuntu")
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{"server.img": "img", "server.qcow2": "qcow2", "notes.txt": "ignored"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	images, err := manager.ListDiskImages()
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 2 || images[0].Name != "ubuntu/server.img" || images[1].Name != "ubuntu/server.qcow2" {
		t.Fatalf("unexpected disk images: %#v", images)
	}
}

func TestVMManagerRebootsOnlyRunningVMs(t *testing.T) {
	manager, hypervisor, _ := newTestVMManager(t)
	vm, err := manager.Create(context.Background(), installerVMOptions("reboot-vm"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Reboot(vm.ID); !errors.Is(err, errVMNotRunning) {
		t.Fatalf("rebooting a stopped VM returned %v", err)
	}
	vm, err = manager.Start(vm.ID)
	if err != nil {
		t.Fatal(err)
	}
	vm, err = manager.Reboot(vm.ID)
	if err != nil {
		t.Fatalf("Reboot returned an error: %v", err)
	}
	if !hypervisor.reset || vm.Status != vmStatusRunning {
		t.Fatalf("running VM was not reset correctly: %#v", vm)
	}
}

func TestVMManagerManagesCloudInitISOThroughoutVMLifecycle(t *testing.T) {
	manager, _, _ := newTestVMManager(t)
	manager.buildCloudInitISO = func(_ context.Context, destination string, options cloudInitISOOptions) error {
		contents := strings.Join([]string{options.InstanceID, options.Hostname, options.UserData}, "\n")
		return os.WriteFile(destination, []byte(contents), 0o640)
	}
	options := installerVMOptions("cloud-vm")
	options.CloudInit = vmCloudInit{Enabled: true, UserData: "#cloud-config\npackages: [curl]\n"}
	vm, err := manager.Create(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	assertCloudInitISOContents(t, manager.cloudInitISOPath(vm.ID), "vm-id\ncloud-vm\n#cloud-config\npackages: [curl]\n")

	vm, err = manager.ConfigureCloudInit(context.Background(), vm.ID, vmCloudInit{Enabled: true, UserData: "#!/bin/sh\necho ready\n"})
	if err != nil {
		t.Fatal(err)
	}
	assertCloudInitISOContents(t, manager.cloudInitISOPath(vm.ID), "vm-id\ncloud-vm\n#!/bin/sh\necho ready\n")
	vm, err = manager.Update(vm.ID, "renamed-cloud-vm", vm.Description, vm.CPUs, vm.MemoryMiB)
	if err != nil {
		t.Fatal(err)
	}
	assertCloudInitISOContents(t, manager.cloudInitISOPath(vm.ID), "vm-id\nrenamed-cloud-vm\n#!/bin/sh\necho ready\n")

	manager.newID = func() (string, error) { return "cloud-clone-id", nil }
	clone, err := manager.Clone(context.Background(), vm.ID, "cloud-vm-copy")
	if err != nil {
		t.Fatal(err)
	}
	if clone.CloudInit != vm.CloudInit {
		t.Fatalf("clone cloud-init configuration = %#v, want %#v", clone.CloudInit, vm.CloudInit)
	}
	assertCloudInitISOContents(t, manager.cloudInitISOPath(clone.ID), "cloud-clone-id\ncloud-vm-copy\n#!/bin/sh\necho ready\n")
	if err := manager.Delete(clone.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(manager.cloudInitISOPath(clone.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted clone retained its cloud-init ISO: %v", err)
	}

	vm, err = manager.Start(vm.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ConfigureCloudInit(context.Background(), vm.ID, vmCloudInit{Enabled: true, UserData: "changed"}); !errors.Is(err, errDeviceRunningVM) {
		t.Fatalf("changing cloud-init on a running VM returned %v", err)
	}
	if _, err := manager.Stop(vm.ID, true, 0); err != nil {
		t.Fatal(err)
	}
	vm, err = manager.ConfigureCloudInit(context.Background(), vm.ID, vmCloudInit{})
	if err != nil || vm.CloudInit.Enabled {
		t.Fatalf("disable cloud-init = %#v, %v", vm.CloudInit, err)
	}
	if _, err := os.Stat(manager.cloudInitISOPath(vm.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled VM retained its cloud-init ISO: %v", err)
	}
}

func assertCloudInitISOContents(t *testing.T, path, expected string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != expected {
		t.Fatalf("cloud-init ISO %s = %q, %v; want %q", path, contents, err, expected)
	}
}

func TestVMManagerClonesStoppedVMWithIndependentIdentityAndDisks(t *testing.T) {
	manager, _, settings := newTestVMManager(t)
	if err := os.WriteFile(filepath.Join(settings.ISODir, "tools.iso"), []byte("tools"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := manager.Create(context.Background(), bridgedVMOptions("source-vm", "br0"))
	if err != nil {
		t.Fatal(err)
	}
	source, err = manager.Update(source.ID, source.Name, "Build template", source.CPUs, source.MemoryMiB)
	if err != nil {
		t.Fatal(err)
	}
	source, err = manager.AddDisk(context.Background(), source.ID, vmDiskSpec{SizeGiB: 5})
	if err != nil {
		t.Fatal(err)
	}
	source, err = manager.AddCDROM(source.ID, "tools.iso")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.vmDiskPath(source.ID, source.Disks[0]), []byte("system source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.vmDiskPath(source.ID, source.Disks[1]), []byte("data source"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	manager.vms[manager.indexByID(source.ID)].IPMI = vmIPMIConfig{Enabled: true, Address: "192.0.2.10", Username: "admin", Password: "secret"}
	manager.mu.Unlock()
	manager.newID = func() (string, error) { return "clone-id", nil }
	manager.now = func() time.Time { return time.Date(2026, 8, 20, 2, 3, 4, 0, time.UTC) }

	clone, err := manager.Clone(context.Background(), source.ID, "source-vm-copy")
	if err != nil {
		t.Fatalf("Clone returned an error: %v", err)
	}
	if clone.ID != "clone-id" || clone.Name != "source-vm-copy" || clone.Status != vmStatusStopped {
		t.Fatalf("unexpected cloned VM identity: %#v", clone)
	}
	if len(clone.NICs) != 1 || clone.NICs[0].MACAddress == source.NICs[0].MACAddress || clone.NICs[0].ID == source.NICs[0].ID {
		t.Fatalf("clone did not receive a new interface identity: source=%#v clone=%#v", source.NICs, clone.NICs)
	}
	if clone.NICs[0].MACAddress != nicMACAddress(clone.ID, clone.NICs[0].ID) {
		t.Fatalf("clone MAC address is not derived from its identity: %#v", clone.NICs[0])
	}
	if clone.CreatedAt.Equal(source.CreatedAt) || clone.Description != source.Description || clone.CPUs != source.CPUs || clone.MemoryMiB != source.MemoryMiB {
		t.Fatalf("clone definition was not copied with a new timestamp: %#v", clone)
	}
	if clone.NICs[0].Mode != source.NICs[0].Mode || clone.NICs[0].BridgeName != source.NICs[0].BridgeName || clone.IPMI.Enabled {
		t.Fatalf("clone network identity was not reset correctly: %#v", clone)
	}
	if len(clone.Disks) != len(source.Disks) || clone.Disks[0].ID == source.Disks[0].ID || clone.Disks[1].ID == source.Disks[1].ID {
		t.Fatalf("clone disk definitions are incorrect: source=%#v clone=%#v", source.Disks, clone.Disks)
	}
	if len(clone.CDROMs) != len(source.CDROMs) || clone.CDROMs[0].ID == source.CDROMs[0].ID || clone.CDROMs[1].ISOName != "tools.iso" {
		t.Fatalf("clone CD-ROM definitions are incorrect: source=%#v clone=%#v", source.CDROMs, clone.CDROMs)
	}
	for index, expected := range []string{"system source", "data source"} {
		contents, err := os.ReadFile(manager.vmDiskPath(clone.ID, clone.Disks[index]))
		if err != nil || string(contents) != expected {
			t.Fatalf("cloned disk %d = %q, %v; want %q", index, contents, err, expected)
		}
	}
	if err := os.WriteFile(manager.vmDiskPath(clone.ID, clone.Disks[0]), []byte("changed clone"), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceContents, err := os.ReadFile(manager.vmDiskPath(source.ID, source.Disks[0]))
	if err != nil || string(sourceContents) != "system source" {
		t.Fatalf("clone disk is not independent of its source: %q, %v", sourceContents, err)
	}
	if _, err := manager.Clone(context.Background(), source.ID, "SOURCE-VM-COPY"); !errors.Is(err, errVMNameExists) {
		t.Fatalf("duplicate clone name returned %v", err)
	}
	if _, err := manager.Start(source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Clone(context.Background(), source.ID, "running-copy"); !errors.Is(err, errCloneRunningVM) {
		t.Fatalf("cloning a running VM returned %v", err)
	}
}

func TestVMManagerRemovesPartialCloneWhenDiskCopyFails(t *testing.T) {
	manager, _, settings := newTestVMManager(t)
	source, err := manager.Create(context.Background(), installerVMOptions("source-vm"))
	if err != nil {
		t.Fatal(err)
	}
	source, err = manager.AddDisk(context.Background(), source.ID, vmDiskSpec{SizeGiB: 5})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(manager.vmDiskPath(source.ID, source.Disks[1])); err != nil {
		t.Fatal(err)
	}
	manager.newID = func() (string, error) { return "clone-id", nil }

	if _, err := manager.Clone(context.Background(), source.ID, "source-copy"); err == nil {
		t.Fatal("clone unexpectedly succeeded with a missing source disk")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(settings.DiskDir, "clone-id-*.qcow2")); len(leftovers) != 0 {
		t.Fatalf("partial disks were not cleaned up: %v", leftovers)
	}
	if _, err := os.Stat(settings.vmStateDir("clone-id")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial state directory was not cleaned up: %v", err)
	}
	vms, err := manager.List()
	if err != nil || len(vms) != 1 {
		t.Fatalf("partial clone metadata was retained: %#v, %v", vms, err)
	}
}

func TestVMManagerUpdatesSettingsAndDevices(t *testing.T) {
	manager, hypervisor, settings := newTestVMManager(t)
	if err := os.WriteFile(filepath.Join(settings.ISODir, "tools.iso"), []byte("iso"), 0o600); err != nil {
		t.Fatal(err)
	}
	vm, err := manager.Create(context.Background(), installerVMOptions("settings-vm"))
	if err != nil {
		t.Fatal(err)
	}
	nic := vm.NICs[0]
	vm, err = manager.UpdateNIC(vm.ID, nic.ID, vmNICSpec{Mode: networkModeBridge, BridgeName: "br0", MACAddress: "52:54:00:aa:bb:cc"})
	if err != nil || vm.NICs[0].Mode != networkModeBridge || vm.NICs[0].BridgeName != "br0" || vm.NICs[0].MACAddress != "52:54:00:aa:bb:cc" {
		t.Fatalf("network interface was not updated: %#v, %v", vm.NICs, err)
	}
	vm, err = manager.Start(vm.ID)
	if err != nil {
		t.Fatal(err)
	}
	vm, err = manager.Update(vm.ID, "renamed-vm", "Build worker", 4, 4096)
	if err != nil || vm.Name != "renamed-vm" || vm.Description != "Build worker" || vm.CPUs != 4 || vm.MemoryMiB != 4096 {
		t.Fatalf("settings were not persisted while running: %#v, %v", vm, err)
	}
	vm, err = manager.UpdateCDROM(vm.ID, vm.CDROMs[0].ID, "tools.iso")
	if err != nil || hypervisor.changedCDROM != vm.CDROMs[0].ID || hypervisor.changedISO != filepath.Join(settings.ISODir, "tools.iso") {
		t.Fatalf("live CD-ROM update was not applied: %#v, %v", hypervisor, err)
	}
	if _, err := manager.AddDisk(context.Background(), vm.ID, vmDiskSpec{SizeGiB: 5}); !errors.Is(err, errDeviceRunningVM) {
		t.Fatalf("adding a running disk returned %v", err)
	}
	if _, err := manager.UpdateNIC(vm.ID, nic.ID, vmNICSpec{Mode: networkModeNAT}); !errors.Is(err, errDeviceRunningVM) {
		t.Fatalf("changing a running NIC returned %v", err)
	}
	if _, err := manager.UpdateNIC(vm.ID, nic.ID, vmNICSpec{Mode: networkModeBridge, BridgeName: "br0", MACAddress: "52:54:00:aa:bb:cc"}); err != nil {
		t.Fatalf("saving an unchanged running NIC returned %v", err)
	}
	if _, err := manager.AddNIC(vm.ID, vmNICSpec{Mode: networkModeNAT}); !errors.Is(err, errDeviceRunningVM) {
		t.Fatalf("adding a NIC to a running VM returned %v", err)
	}
	if _, err := manager.Stop(vm.ID, true, 0); err != nil {
		t.Fatal(err)
	}
	vm, err = manager.AddDisk(context.Background(), vm.ID, vmDiskSpec{SizeGiB: 5})
	if err != nil || len(vm.Disks) != 2 {
		t.Fatalf("data disk was not added: %#v, %v", vm, err)
	}
	dataDisk := vm.Disks[1]
	if _, err := os.Stat(manager.vmDiskPath(vm.ID, dataDisk)); err != nil {
		t.Fatalf("data disk file is missing: %v", err)
	}
	vm, err = manager.RemoveDisk(vm.ID, dataDisk.ID)
	if err != nil || len(vm.Disks) != 1 {
		t.Fatalf("data disk was not removed: %#v, %v", vm, err)
	}
	vm, err = manager.AddCDROM(vm.ID, "")
	if err != nil || len(vm.CDROMs) != 2 || vm.CDROMs[1].ISOName != "" {
		t.Fatalf("empty CD-ROM was not added: %#v, %v", vm, err)
	}
	vm, err = manager.AddNIC(vm.ID, vmNICSpec{Mode: networkModeBridge, BridgeName: "br1"})
	if err != nil || len(vm.NICs) != 2 || vm.NICs[1].MACAddress == "" || vm.NICs[1].MACAddress == vm.NICs[0].MACAddress {
		t.Fatalf("second NIC was not added with its own MAC address: %#v, %v", vm.NICs, err)
	}
	if bridges := vm.bridges(); !slices.Equal(bridges, []string{"br0", "br1"}) {
		t.Fatalf("bridges = %v, want [br0 br1]", bridges)
	}
	if _, err := manager.AddNIC(vm.ID, vmNICSpec{Mode: networkModeNAT, MACAddress: "52:54:00:AA:BB:CC"}); err == nil {
		t.Fatal("duplicate MAC address was accepted")
	}
	vm, err = manager.RemoveNIC(vm.ID, vm.NICs[0].ID)
	if err != nil || len(vm.NICs) != 1 || vm.NICs[0].BridgeName != "br1" {
		t.Fatalf("NIC was not removed: %#v, %v", vm.NICs, err)
	}
	vm, err = manager.RemoveDisk(vm.ID, vm.Disks[0].ID)
	if err != nil || len(vm.Disks) != 0 {
		t.Fatalf("last disk could not be removed: %#v, %v", vm.Disks, err)
	}
}

func TestVMManagerPersistsDefinitions(t *testing.T) {
	workingDirectory := t.TempDir()
	settings := defaultConfig(workingDirectory)
	if err := prepareDirectories(settings); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settings.ISODir, "installer.iso"), []byte("iso"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newJSONVMStore(settings.StateDir)
	hypervisor := newFakeHypervisor()
	options := vmManagerOptions{Settings: settings, Store: store, Hypervisor: hypervisor, Capabilities: testCapabilities}
	manager, err := newVMManager(options)
	if err != nil {
		t.Fatal(err)
	}
	manager.newID = func() (string, error) { return "persistent-id", nil }
	if _, err := manager.Create(context.Background(), bridgedVMOptions("Persistent", "br0")); err != nil {
		t.Fatal(err)
	}

	reloaded, err := newVMManager(options)
	if err != nil {
		t.Fatal(err)
	}
	vms, err := reloaded.List()
	if err != nil || len(vms) != 1 || vms[0].ID != "persistent-id" || len(vms[0].NICs) != 1 || vms[0].NICs[0].BridgeName != "br0" {
		t.Fatalf("VM definition was not reloaded: %#v, %v", vms, err)
	}
}

func TestVMManagerRejectsUnavailableBridgeAtStart(t *testing.T) {
	manager, _, _ := newTestVMManager(t)
	manager.bridgeCapability = func(string) (bool, string) { return false, "bridge br0 is unavailable" }
	vm, err := manager.Create(context.Background(), bridgedVMOptions("bridge-vm", "br0"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(vm.ID); !errors.Is(err, errBridgeUnavailable) {
		t.Fatalf("expected bridge unavailable error, got %v", err)
	}
}

func TestVMManagerCoordinatesBridgeNetworkLifecycle(t *testing.T) {
	manager, _, _ := newTestVMManager(t)
	lifecycle := &fakeVMNetworkLifecycle{}
	manager.networkLifecycle = lifecycle
	vm, err := manager.Create(context.Background(), bridgedVMOptions("dhcp-vm", "br0"))
	if err != nil {
		t.Fatal(err)
	}
	if vm.NICs[0].MACAddress == "" {
		t.Fatal("created VM did not receive a stable MAC address")
	}
	if _, err := manager.Start(vm.ID); err != nil {
		t.Fatal(err)
	}
	if len(lifecycle.prepared) != 1 || lifecycle.prepared[0] != vm.ID {
		t.Fatalf("network was not prepared before start: %#v", lifecycle.prepared)
	}
	if _, err := manager.Stop(vm.ID, true, 0); err != nil {
		t.Fatal(err)
	}
	if len(lifecycle.released) != 1 || lifecycle.released[0] != vm.ID {
		t.Fatalf("network was not released after force stop: %#v", lifecycle.released)
	}
}

func TestVMManagerListsOnlyISOFiles(t *testing.T) {
	manager, _, settings := newTestVMManager(t)
	if err := os.WriteFile(filepath.Join(settings.ISODir, "README.txt"), []byte("ignore"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settings.ISODir, "SECOND.ISO"), []byte("iso2"), 0o600); err != nil {
		t.Fatal(err)
	}

	images, err := manager.ListISOs()
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 2 || images[0].Name != "installer.iso" || images[1].Name != "SECOND.ISO" {
		t.Fatalf("unexpected ISO list: %#v", images)
	}
}

func TestVMManagerReportsSymlinkedISOTargetSize(t *testing.T) {
	manager, _, settings := newTestVMManager(t)
	targetPath := filepath.Join(t.TempDir(), "large-installer.iso")
	const targetSize = int64(128 * 1024 * 1024)
	if err := os.WriteFile(targetPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(targetPath, targetSize); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetPath, filepath.Join(settings.ISODir, "linked.iso")); err != nil {
		t.Fatal(err)
	}

	images, err := manager.ListISOs()
	if err != nil {
		t.Fatal(err)
	}
	linkedIndex := slices.IndexFunc(images, func(image isoImage) bool { return image.Name == "linked.iso" })
	if linkedIndex == -1 {
		t.Fatalf("symlinked ISO is missing from the list: %#v", images)
	}
	if images[linkedIndex].SizeBytes != uint64(targetSize) {
		t.Fatalf("symlinked ISO size = %d, want target size %d", images[linkedIndex].SizeBytes, targetSize)
	}
}

func TestVMManagerRejectsNamesThatCannotBeDNSLabels(t *testing.T) {
	manager, _, _ := newTestVMManager(t)
	for _, name := range []string{"has spaces", "-leading", "trailing-", "under_score"} {
		_, err := manager.Create(context.Background(), installerVMOptions(name))
		var fieldErr *fieldError
		if !errors.As(err, &fieldErr) || fieldErr.field != "name" {
			t.Fatalf("invalid DNS name %q was not rejected as a name field error: %v", name, err)
		}
	}
}

func TestVMManagerRejectsInvalidDevices(t *testing.T) {
	manager, _, _ := newTestVMManager(t)
	for name, options := range map[string]createVMOptions{
		"oversized disk":       {Name: "a", CPUs: 1, MemoryMiB: 512, Disks: []vmDiskSpec{{SizeGiB: 4096}}},
		"missing cloud image":  {Name: "b", CPUs: 1, MemoryMiB: 512, Disks: []vmDiskSpec{{SizeGiB: 10, SourceImage: "missing.img"}}},
		"missing ISO":          {Name: "c", CPUs: 1, MemoryMiB: 512, CDROMs: []vmCDROMSpec{{ISOName: "missing.iso"}}},
		"bridge without name":  {Name: "d", CPUs: 1, MemoryMiB: 512, NICs: []vmNICSpec{{Mode: networkModeBridge}}},
		"unknown network mode": {Name: "e", CPUs: 1, MemoryMiB: 512, NICs: []vmNICSpec{{Mode: "vlan"}}},
		"multicast MAC":        {Name: "f", CPUs: 1, MemoryMiB: 512, NICs: []vmNICSpec{{Mode: networkModeNAT, MACAddress: "01:00:00:00:00:01"}}},
	} {
		_, err := manager.Create(context.Background(), options)
		var fieldErr *fieldError
		if !errors.As(err, &fieldErr) {
			t.Errorf("%s was not rejected as a field error: %v", name, err)
		}
	}
	if vms, _ := manager.List(); len(vms) != 0 {
		t.Fatalf("rejected VMs were created: %#v", vms)
	}
}

func TestForceStopWaitsForExitBeforeRestart(t *testing.T) {
	manager, hypervisor, _ := newTestVMManager(t)
	vm, err := manager.Create(context.Background(), installerVMOptions("cycle"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(vm.ID); err != nil {
		t.Fatal(err)
	}
	stopped, err := manager.Stop(vm.ID, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Status != vmStatusStopped || stopped.PID != 0 || stopped.LastError != "" {
		t.Fatalf("forced stop should wait for the exit without recording an error: %#v", stopped)
	}
	if _, ok := manager.exits[vm.ID]; ok {
		t.Fatal("exit signal was not cleared")
	}
	restarted, err := manager.Start(vm.ID)
	if err != nil {
		t.Fatalf("restart after forced stop: %v", err)
	}
	if restarted.Status != vmStatusRunning || !hypervisor.IsRunning(restarted) {
		t.Fatalf("VM did not restart: %#v", restarted)
	}
}

func TestForceStopTimesOutWithoutExitCallback(t *testing.T) {
	manager, hypervisor, _ := newTestVMManager(t)
	manager.forceStopTimeout = 10 * time.Millisecond
	vm, err := manager.Create(context.Background(), installerVMOptions("stuck"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(vm.ID); err != nil {
		t.Fatal(err)
	}
	hypervisor.onExit = nil // the reaper never reports
	stopped, err := manager.Stop(vm.ID, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Status != vmStatusStopped || stopped.PID != 0 {
		t.Fatalf("forced stop should give up waiting and report stopped: %#v", stopped)
	}
}

func TestVMManagerStaysResponsiveWhileCloningDisks(t *testing.T) {
	manager, _, _ := newTestVMManager(t)
	source, err := manager.Create(context.Background(), installerVMOptions("template"))
	if err != nil {
		t.Fatal(err)
	}
	copying := make(chan struct{})
	release := make(chan struct{})
	manager.copyFile = func(ctx context.Context, sourcePath, destinationPath string) error {
		close(copying)
		<-release
		return copyVMFile(ctx, sourcePath, destinationPath)
	}
	manager.newID = func() (string, error) { return "clone-id", nil }
	cloneResult := make(chan error, 1)
	go func() {
		_, err := manager.Clone(context.Background(), source.ID, "template-copy")
		cloneResult <- err
	}()
	<-copying

	// The copy is in progress with the lock released: reads succeed, the
	// clone is not yet listed, and the source is protected from changes.
	vms, err := manager.List()
	if err != nil || len(vms) != 1 {
		t.Fatalf("List during clone = %#v, %v", vms, err)
	}
	if _, err := manager.Start(source.ID); !errors.Is(err, errVMBusy) {
		t.Fatalf("starting the clone source returned %v, want busy", err)
	}
	if err := manager.Delete(source.ID); !errors.Is(err, errVMBusy) {
		t.Fatalf("deleting the clone source returned %v, want busy", err)
	}
	if _, err := manager.Create(context.Background(), installerVMOptions("template-copy")); !errors.Is(err, errVMNameExists) {
		t.Fatalf("reusing the reserved clone name returned %v", err)
	}
	close(release)
	if err := <-cloneResult; err != nil {
		t.Fatalf("Clone returned an error: %v", err)
	}
	vms, err = manager.List()
	if err != nil || len(vms) != 2 {
		t.Fatalf("clone was not listed after completion: %#v, %v", vms, err)
	}
	if _, err := manager.Start(source.ID); err != nil {
		t.Fatalf("source remained busy after the clone finished: %v", err)
	}
}

func TestGracefulStopEscalatesToForceAfterTimeout(t *testing.T) {
	manager, hypervisor, _ := newTestVMManager(t)
	vm, err := manager.Create(context.Background(), installerVMOptions("stubborn"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(vm.ID); err != nil {
		t.Fatal(err)
	}
	stopping, err := manager.Stop(vm.ID, false, 20*time.Millisecond)
	if err != nil || stopping.Status != vmStatusStopping || hypervisor.forcedStop {
		t.Fatalf("graceful stop = %#v, %v (forced=%v)", stopping, err, hypervisor.forcedStop)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		current, err := manager.Get(vm.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status == vmStatusStopped {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("VM was not forced off after the shutdown timeout: %#v", current)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !hypervisor.forcedStop {
		t.Fatal("timeout expiry did not force the VM off")
	}

	// A guest that shuts down in time is never forced.
	hypervisor.forcedStop = false
	if _, err := manager.Start(vm.ID); err != nil {
		t.Fatal(err)
	}
	stopping, err = manager.Stop(vm.ID, false, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	hypervisor.running[stopping.PID] = false
	hypervisor.onExit(stopping.PID, nil)
	time.Sleep(80 * time.Millisecond)
	if hypervisor.forcedStop {
		t.Fatal("a guest that exited before the deadline was still forced off")
	}
}
