package ipmi

import (
	"context"
	"net/netip"
	"strings"
	"testing"

	"github.com/bougou/go-ipmi/pkg/hal"
	"github.com/bougou/go-ipmi/pkg/types"

	"github.com/roflware/app-runner/internal/vm"
	"github.com/roflware/app-runner/internal/vm/vmtest"
)

func TestVMIPMIHALPersistsSupportedBootFlags(t *testing.T) {
	manager, _, _ := vmtest.NewManager(t)
	machine, err := manager.Create(context.Background(), vmtest.InstallerOptions("boot-machine"))
	if err != nil {
		t.Fatal(err)
	}
	hardware := &chassisHAL{manager: manager, vmID: machine.ID}
	flags := &types.BootOptionParam_BootFlags{BootFlagsValid: true, Persist: true, BootDeviceSelector: types.BootDeviceSelectorForcePXE}
	if err := hardware.SetBootFlags(context.Background(), flags); err != nil {
		t.Fatal(err)
	}
	stored, err := hardware.GetBootFlags(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Persist || stored.BootDeviceSelector != types.BootDeviceSelectorForcePXE {
		t.Fatalf("unexpected stored boot flags: %#v", stored)
	}
	flags.BootDeviceSelector = types.BootDeviceSelectorForceBIOSSetup
	if err := hardware.SetBootFlags(context.Background(), flags); err != hal.ErrNotSupported {
		t.Fatalf("unsupported boot selector returned %v", err)
	}
}

func TestIPMIManagerLeavesUnchangedListenerAlone(t *testing.T) {
	manager := NewManager(nil, nil)
	config := vm.IPMIConfig{Enabled: true, BridgeName: "no-such-bridge", Address: "192.0.2.10", Username: "admin", Password: "secret"}
	manager.runtimes["machine-id"] = &endpoint{
		bridge: config.BridgeName, address: netip.MustParseAddr(config.Address), prefixBits: 24,
		username: config.Username, password: config.Password, running: true,
	}
	// Identical configuration returns before consulting the (absent) bridge.
	if err := manager.Configure(vm.VirtualMachine{ID: "machine-id", IPMI: config}); err != nil {
		t.Fatalf("unchanged configuration was not a no-op: %v", err)
	}
	if running, _ := manager.Status("machine-id"); !running {
		t.Fatal("unchanged configuration stopped the listener")
	}
	// A credential rotation must go through, which here fails on the missing bridge.
	rotated := config
	rotated.Password = "rotated"
	if err := manager.Configure(vm.VirtualMachine{ID: "machine-id", IPMI: rotated}); err == nil || !strings.Contains(err.Error(), "bridge") {
		t.Fatalf("changed configuration did not attempt reconfiguration: %v", err)
	}
}
