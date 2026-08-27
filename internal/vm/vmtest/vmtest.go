// Package vmtest provides fakes and a ready-made VM manager for tests in
// packages that build on the VM manager.
package vmtest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/roflware/app-runner/internal/config"
	"github.com/roflware/app-runner/internal/vm"
)

// MemoryStore keeps VM definitions in memory.
type MemoryStore struct {
	VMs []vm.VirtualMachine
}

func (s *MemoryStore) Load() ([]vm.VirtualMachine, error) {
	return slices.Clone(s.VMs), nil
}

func (s *MemoryStore) Save(vms []vm.VirtualMachine) error {
	s.VMs = slices.Clone(vms)
	return nil
}

// FakeHypervisor records lifecycle calls and creates disk files without
// qemu-img.
type FakeHypervisor struct {
	Running         map[int]bool
	NextPID         int
	GracefulStopped bool
	ForcedStop      bool
	ResetCalled     bool
	OnExit          func(int, error)
	ChangedCDROM    string
	ChangedISO      string
	LinkedSource    string
}

func NewFakeHypervisor() *FakeHypervisor {
	return &FakeHypervisor{Running: map[int]bool{}, NextPID: 1200}
}

func (h *FakeHypervisor) CreateDisk(_ context.Context, path string, _ uint32) error {
	return os.WriteFile(path, []byte("qcow2"), 0o600)
}

func (h *FakeHypervisor) CreateLinkedDisk(_ context.Context, path, backingPath string, _ uint32) error {
	h.LinkedSource = backingPath
	return os.WriteFile(path, []byte("linked qcow2"), 0o600)
}

func (h *FakeHypervisor) Start(_ vm.VirtualMachine, onExit func(int, error)) (int, error) {
	h.NextPID++
	h.Running[h.NextPID] = true
	h.OnExit = onExit
	return h.NextPID, nil
}

func (h *FakeHypervisor) IsRunning(machine vm.VirtualMachine) bool {
	return h.Running[machine.PID]
}

func (h *FakeHypervisor) GracefulStop(vm.VirtualMachine) error {
	h.GracefulStopped = true
	return nil
}

func (h *FakeHypervisor) ForceStop(machine vm.VirtualMachine) error {
	h.ForcedStop = true
	delete(h.Running, machine.PID)
	if h.OnExit != nil {
		// QEMU's reaper reports the exit asynchronously, as the real hypervisor does.
		go h.OnExit(machine.PID, errors.New("signal: killed"))
	}
	return nil
}

func (h *FakeHypervisor) Reset(vm.VirtualMachine) error {
	h.ResetCalled = true
	return nil
}

func (h *FakeHypervisor) ChangeCDROMMedia(_ vm.VirtualMachine, cdromID, isoPath string) error {
	h.ChangedCDROM = cdromID
	h.ChangedISO = isoPath
	return nil
}

// Capabilities reports a host with QEMU and KVM available.
func Capabilities() vm.HostCapabilities {
	return vm.HostCapabilities{QEMUAvailable: true, KVMAvailable: true}
}

// NewManager builds a VM manager over temporary directories that already
// contain installer.iso, with fixed identities and clock.
func NewManager(t *testing.T) (*vm.Manager, *FakeHypervisor, config.Config) {
	t.Helper()
	settings := config.Default(t.TempDir())
	if err := config.PrepareDirectories(settings); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settings.ISODir, "installer.iso"), []byte("iso"), 0o600); err != nil {
		t.Fatal(err)
	}
	hypervisor := NewFakeHypervisor()
	manager, err := vm.NewManager(vm.ManagerOptions{
		Settings: settings, Store: &MemoryStore{}, Hypervisor: hypervisor, Capabilities: Capabilities,
		BridgeCapability: func(string) (bool, string) { return true, "" },
		NewID:            func() (string, error) { return "vm-id", nil },
		Now:              func() time.Time { return time.Date(2026, 8, 19, 1, 2, 3, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager, hypervisor, settings
}

// InstallerOptions describes a conventional VM: one disk, one installer
// CD-ROM, and one NAT interface.
func InstallerOptions(name string) vm.CreateOptions {
	return vm.CreateOptions{
		Name: name, CPUs: 2, MemoryMiB: 2048,
		Disks:  []vm.DiskSpec{{SizeGiB: 20}},
		CDROMs: []vm.CDROMSpec{{ISOName: "installer.iso"}},
		NICs:   []vm.NICSpec{{Mode: vm.NetworkModeNAT}},
	}
}

// BridgedOptions is InstallerOptions with a single bridged interface.
func BridgedOptions(name, bridge string) vm.CreateOptions {
	options := InstallerOptions(name)
	options.NICs = []vm.NICSpec{{Mode: vm.NetworkModeBridge, BridgeName: bridge}}
	return options
}
