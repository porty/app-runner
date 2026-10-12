package ipmi

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/bougou/go-ipmi/pkg/bmc"
	"github.com/bougou/go-ipmi/pkg/client"
	"github.com/bougou/go-ipmi/pkg/hal"
	"github.com/bougou/go-ipmi/pkg/transport/udp"
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

func TestBMCPrefersCipherSuite17AndAcceptsSuite3(t *testing.T) {
	if len(cipherSuites) != 2 || cipherSuites[0] != types.CipherSuiteID17 || cipherSuites[1] != types.CipherSuiteID3 {
		t.Fatalf("cipher suites = %v, want 17 then 3", cipherSuites)
	}
	for _, suite := range cipherSuites {
		if !bmc.SupportedCipherSuite(suite) {
			t.Fatalf("cipher suite %d is not implemented by the go-ipmi server", suite)
		}
	}

	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	bmcServer, err := newBMCServer("machine-id", &chassisHAL{}, "admin", "secret", udp.Wrap(listener, udp.WithReadTimeout(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- bmcServer.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = bmcServer.Close()
		<-served
	})
	port := listener.LocalAddr().(*net.UDPAddr).Port

	connect := func(suite types.CipherSuiteID) error {
		ipmiClient, err := client.NewClient("127.0.0.1", port, "admin", "secret")
		if err != nil {
			return err
		}
		ipmiClient.WithCipherSuiteID(suite).WithTimeout(2 * time.Second).WithRetry(0)
		if err := ipmiClient.Connect(ctx); err != nil {
			return err
		}
		defer ipmiClient.Close(ctx)
		_, err = ipmiClient.GetDeviceID(ctx)
		return err
	}
	for _, suite := range []types.CipherSuiteID{types.CipherSuiteID17, types.CipherSuiteID3} {
		if err := connect(suite); err != nil {
			t.Errorf("lanplus session with cipher suite %d failed: %v", suite, err)
		}
	}
	for _, suite := range []types.CipherSuiteID{types.CipherSuiteID0, types.CipherSuiteID2} {
		if err := connect(suite); err == nil {
			t.Errorf("lanplus session with unconfigured cipher suite %d was accepted", suite)
		}
	}
}
