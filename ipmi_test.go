package main

import (
	"context"
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
	vm, err := manager.Create(context.Background(), createVMOptions{
		Name: "ipmi-vm", CPUs: 2, MemoryMiB: 2048, DiskGiB: 20, ISOName: "installer.iso", NetworkMode: networkModeNAT,
	})
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
	vm, err := manager.Create(context.Background(), createVMOptions{
		Name: "boot-vm", CPUs: 2, MemoryMiB: 2048, DiskGiB: 20, ISOName: "installer.iso", NetworkMode: networkModeNAT,
	})
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
