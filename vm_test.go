package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
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
	onExit       func(error)
}

func newFakeHypervisor() *fakeHypervisor {
	return &fakeHypervisor{running: map[int]bool{}, nextPID: 1200}
}

func (h *fakeHypervisor) CreateDisk(_ context.Context, path string, _ uint32) error {
	return os.WriteFile(path, []byte("qcow2"), 0o600)
}

func (h *fakeHypervisor) Start(_ virtualMachine, onExit func(error)) (int, error) {
	h.nextPID++
	h.running[h.nextPID] = true
	h.onExit = onExit
	return h.nextPID, nil
}

func (h *fakeHypervisor) IsRunning(pid int) bool {
	return h.running[pid]
}

func (h *fakeHypervisor) GracefulStop(vm virtualMachine) error {
	h.gracefulStop = true
	return nil
}

func (h *fakeHypervisor) ForceStop(vm virtualMachine) error {
	h.forcedStop = true
	delete(h.running, vm.PID)
	return nil
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
	manager, err := newVMManager(settings, &memoryVMStore{}, hypervisor, func() hostCapabilities {
		return hostCapabilities{QEMUAvailable: true, KVMAvailable: true, BridgeAvailable: true, BridgeName: "br0"}
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.newID = func() (string, error) { return "vm-id", nil }
	manager.bridgeCapability = func(string) (bool, string) { return true, "" }
	manager.now = func() time.Time { return time.Date(2026, 8, 19, 1, 2, 3, 0, time.UTC) }
	return manager, hypervisor, settings
}

func TestVMManagerLifecycle(t *testing.T) {
	manager, hypervisor, settings := newTestVMManager(t)
	vm, err := manager.Create(context.Background(), createVMOptions{
		Name: "Development", CPUs: 2, MemoryMiB: 2048, DiskGiB: 20,
		ISOName: "installer.iso", NetworkMode: networkModeNAT,
	})
	if err != nil {
		t.Fatalf("Create returned an error: %v", err)
	}
	if vm.Status != vmStatusStopped || vm.ID != "vm-id" {
		t.Fatalf("unexpected created VM: %#v", vm)
	}
	if _, err := os.Stat(filepath.Join(settings.DiskDir, "vm-id.qcow2")); err != nil {
		t.Fatalf("system disk was not created: %v", err)
	}

	vm, err = manager.Start(vm.ID)
	if err != nil {
		t.Fatalf("Start returned an error: %v", err)
	}
	if vm.Status != vmStatusRunning || vm.PID == 0 {
		t.Fatalf("VM was not marked running: %#v", vm)
	}

	vm, err = manager.Stop(vm.ID, false)
	if err != nil {
		t.Fatalf("graceful Stop returned an error: %v", err)
	}
	if !hypervisor.gracefulStop || vm.Status != vmStatusStopping {
		t.Fatalf("graceful stop was not requested: %#v", vm)
	}
	hypervisor.running[vm.PID] = false
	hypervisor.onExit(nil)

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

func TestVMManagerPersistsDefinitions(t *testing.T) {
	workingDirectory := t.TempDir()
	settings := defaultConfig(workingDirectory)
	if err := prepareDirectories(settings); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settings.ISODir, "installer.iso"), []byte("iso"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newJSONVMStore(settings.DiskDir)
	hypervisor := newFakeHypervisor()
	capabilities := func() hostCapabilities {
		return hostCapabilities{QEMUAvailable: true, KVMAvailable: true, BridgeAvailable: true}
	}
	manager, err := newVMManager(settings, store, hypervisor, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	manager.newID = func() (string, error) { return "persistent-id", nil }
	if _, err := manager.Create(context.Background(), createVMOptions{
		Name: "Persistent", CPUs: 1, MemoryMiB: 1024, DiskGiB: 10,
		ISOName: "installer.iso", NetworkMode: networkModeBridge, BridgeName: "br0",
	}); err != nil {
		t.Fatal(err)
	}

	reloaded, err := newVMManager(settings, store, hypervisor, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	vms, err := reloaded.List()
	if err != nil || len(vms) != 1 || vms[0].ID != "persistent-id" {
		t.Fatalf("VM definition was not reloaded: %#v, %v", vms, err)
	}
}

func TestVMManagerMigratesLegacyBridgeName(t *testing.T) {
	workingDirectory := t.TempDir()
	settings := defaultConfig(workingDirectory)
	settings.BridgeName = "legacy0"
	if err := prepareDirectories(settings); err != nil {
		t.Fatal(err)
	}
	store := &memoryVMStore{vms: []virtualMachine{{
		ID: "legacy-vm", Name: "Legacy", NetworkMode: networkModeBridge, Status: vmStatusStopped,
	}}}
	manager, err := newVMManager(settings, store, newFakeHypervisor(), func() hostCapabilities {
		return hostCapabilities{QEMUAvailable: true, KVMAvailable: true, BridgeName: "legacy0"}
	})
	if err != nil {
		t.Fatal(err)
	}
	vms, err := manager.List()
	if err != nil {
		t.Fatal(err)
	}
	if vms[0].BridgeName != "legacy0" || store.vms[0].BridgeName != "legacy0" {
		t.Fatalf("legacy bridge name was not persisted: %#v", vms[0])
	}
}

func TestVMManagerRejectsUnavailableBridgeAtStart(t *testing.T) {
	manager, _, _ := newTestVMManager(t)
	manager.bridgeCapability = func(string) (bool, string) { return false, "bridge br0 is unavailable" }
	vm, err := manager.Create(context.Background(), createVMOptions{
		Name: "Bridge VM", CPUs: 1, MemoryMiB: 1024, DiskGiB: 10,
		ISOName: "installer.iso", NetworkMode: networkModeBridge, BridgeName: "br0",
	})
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
	vm, err := manager.Create(context.Background(), createVMOptions{
		Name: "DHCP VM", CPUs: 1, MemoryMiB: 1024, DiskGiB: 10,
		ISOName: "installer.iso", NetworkMode: networkModeBridge, BridgeName: "br0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if vm.MACAddress == "" {
		t.Fatal("created VM did not receive a stable MAC address")
	}
	if _, err := manager.Start(vm.ID); err != nil {
		t.Fatal(err)
	}
	if len(lifecycle.prepared) != 1 || lifecycle.prepared[0] != vm.ID {
		t.Fatalf("network was not prepared before start: %#v", lifecycle.prepared)
	}
	if _, err := manager.Stop(vm.ID, true); err != nil {
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
