package apprunner

import (
	"context"
	"net/netip"
	"strings"
	"testing"

	"github.com/bougou/go-ipmi/pkg/hal"
	"github.com/bougou/go-ipmi/pkg/types"
)

type fakeIPMIController struct {
	configured []virtualMachine
	disabled   []virtualMachine
}

func (c *fakeIPMIController) Configure(vm virtualMachine) error {
	c.configured = append(c.configured, vm)
	return nil
}
func (c *fakeIPMIController) Disable(vm virtualMachine)    { c.disabled = append(c.disabled, vm) }
func (c *fakeIPMIController) Status(string) (bool, string) { return true, "" }

func TestVMManagerConfiguresAndRetainsIPMICredentials(t *testing.T) {
	manager, _, _ := newTestVMManager(t)
	controller := &fakeIPMIController{}
	manager.ipmi = controller
	vm, err := manager.Create(context.Background(), installerVMOptions("ipmi-vm"))
	if err != nil {
		t.Fatal(err)
	}
	configured, err := manager.ConfigureIPMI(vm.ID, vmIPMIConfig{
		Enabled: true, BridgeName: "br0", Address: "192.168.50.2", Username: "admin", Password: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !configured.IPMI.Enabled || configured.IPMI.Password != "secret" || len(controller.configured) != 1 {
		t.Fatalf("unexpected IPMI configuration: %#v controller=%#v", configured.IPMI, controller)
	}
	configured, err = manager.ConfigureIPMI(vm.ID, vmIPMIConfig{
		Enabled: true, BridgeName: "br0", Address: "192.168.50.3", Username: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if configured.IPMI.Password != "secret" {
		t.Fatalf("blank password did not retain the credential: %#v", configured.IPMI)
	}
}

func TestVMIPMIHALPersistsSupportedBootFlags(t *testing.T) {
	manager, _, _ := newTestVMManager(t)
	vm, err := manager.Create(context.Background(), installerVMOptions("boot-vm"))
	if err != nil {
		t.Fatal(err)
	}
	hardware := &vmIPMIHAL{manager: manager, vmID: vm.ID}
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

func TestValidateIPMIConfigRejectsInvalidEndpoint(t *testing.T) {
	err := validateIPMIConfig(vmIPMIConfig{Enabled: true, BridgeName: "br0", Address: "not-an-ip", Username: "admin", Password: "secret"})
	if err == nil {
		t.Fatal("invalid IPMI address was accepted")
	}
}

func TestIPMIManagerLeavesUnchangedListenerAlone(t *testing.T) {
	manager := newIPMIManager(nil, nil)
	config := vmIPMIConfig{Enabled: true, BridgeName: "no-such-bridge", Address: "192.0.2.10", Username: "admin", Password: "secret"}
	manager.runtimes["vm-id"] = &ipmiRuntime{
		bridge: config.BridgeName, address: netip.MustParseAddr(config.Address), prefixBits: 24,
		username: config.Username, password: config.Password, running: true,
	}
	// Identical configuration returns before consulting the (absent) bridge.
	if err := manager.Configure(virtualMachine{ID: "vm-id", IPMI: config}); err != nil {
		t.Fatalf("unchanged configuration was not a no-op: %v", err)
	}
	if running, _ := manager.Status("vm-id"); !running {
		t.Fatal("unchanged configuration stopped the listener")
	}
	// A credential rotation must go through, which here fails on the missing bridge.
	rotated := config
	rotated.Password = "rotated"
	if err := manager.Configure(virtualMachine{ID: "vm-id", IPMI: rotated}); err == nil || !strings.Contains(err.Error(), "bridge") {
		t.Fatalf("changed configuration did not attempt reconfiguration: %v", err)
	}
}
