package hostnet

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/roflware/app-runner/internal/config"
	"github.com/roflware/app-runner/internal/dhcp"
	"github.com/roflware/app-runner/internal/dns"
	"github.com/roflware/app-runner/internal/validate"
	"github.com/roflware/app-runner/internal/vm"
	"github.com/roflware/app-runner/internal/vm/vmtest"
)

// fakeDHCPBridgeProvider accepts any DHCP range without touching the host.
type fakeDHCPBridgeProvider struct{}

func (fakeDHCPBridgeProvider) ValidateBridgeDHCP(string, dhcp.Range) error      { return nil }
func (fakeDHCPBridgeProvider) EnsureBridgeDHCPAddress(string, dhcp.Range) error { return nil }

type fakeNetworkProvider struct {
	status       Status
	snapshot     Snapshot
	applied      []Change
	restoreCount int
	restored     chan struct{}
	descriptions map[string]string
}

func (p *fakeNetworkProvider) Inspect() (Status, error) {
	return p.status, nil
}

func (p *fakeNetworkProvider) Snapshot(change Change) (Snapshot, error) {
	p.snapshot.Change = change
	return p.snapshot, nil
}

func (p *fakeNetworkProvider) Apply(change Change) error {
	p.applied = append(p.applied, change)
	return nil
}

func (p *fakeNetworkProvider) Restore(Snapshot) error {
	p.restoreCount++
	if p.restored != nil {
		select {
		case p.restored <- struct{}{}:
		default:
		}
	}
	return nil
}

func (p *fakeNetworkProvider) SetBridgeDescription(name, description string) error {
	if p.descriptions == nil {
		p.descriptions = make(map[string]string)
	}
	p.descriptions[name] = description
	for index := range p.status.Bridges {
		if p.status.Bridges[index].Name == name {
			p.status.Bridges[index].Description = description
		}
	}
	return nil
}

func TestNetworkManagerConfirmsChangeBeforeTimeout(t *testing.T) {
	vms, _, settings := vmtest.NewManager(t)
	provider := &fakeNetworkProvider{}
	manager, err := NewManager(ManagerOptions{Provider: provider, VMs: vms, StateDirectory: settings.StateDir})
	if err != nil {
		t.Fatal(err)
	}
	manager.confirmationWindow = 30 * time.Millisecond
	manager.newID = func() (string, error) { return "change-id", nil }

	pending, err := manager.Apply(t.Context(), Change{Type: ChangeCreateBridge, BridgeName: "br1"})
	if err != nil {
		t.Fatal(err)
	}
	if pending.ID != "change-id" || len(provider.applied) != 1 {
		t.Fatalf("change was not applied: %#v", pending)
	}
	if err := manager.Confirm(pending.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if provider.restoreCount != 0 {
		t.Fatalf("confirmed change was reverted %d times", provider.restoreCount)
	}
	if _, err := os.Stat(manager.statePath); !os.IsNotExist(err) {
		t.Fatalf("pending state was not removed: %v", err)
	}
}

func TestNetworkManagerRevertsAfterTimeout(t *testing.T) {
	vms, _, settings := vmtest.NewManager(t)
	provider := &fakeNetworkProvider{restored: make(chan struct{}, 1)}
	manager, err := NewManager(ManagerOptions{Provider: provider, VMs: vms, StateDirectory: settings.StateDir})
	if err != nil {
		t.Fatal(err)
	}
	manager.confirmationWindow = 20 * time.Millisecond
	manager.newID = func() (string, error) { return "change-id", nil }
	if _, err := manager.Apply(t.Context(), Change{Type: ChangeSetBridgeUp, BridgeName: "br1"}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-provider.restored:
	case <-time.After(time.Second):
		t.Fatal("network change was not reverted after its timeout")
	}
	if provider.restoreCount != 1 {
		t.Fatalf("expected one restore, got %d", provider.restoreCount)
	}
}

func TestNetworkManagerRevertsPendingChangeOnRestart(t *testing.T) {
	vms, _, settings := vmtest.NewManager(t)
	provider := &fakeNetworkProvider{}
	manager, err := NewManager(ManagerOptions{Provider: provider, VMs: vms, StateDirectory: settings.StateDir})
	if err != nil {
		t.Fatal(err)
	}
	manager.confirmationWindow = time.Hour
	manager.newID = func() (string, error) { return "change-id", nil }
	if _, err := manager.Apply(t.Context(), Change{Type: ChangeCreateBridge, BridgeName: "br1"}); err != nil {
		t.Fatal(err)
	}
	manager.timer.Stop()

	restartedProvider := &fakeNetworkProvider{}
	if _, err := NewManager(ManagerOptions{Provider: restartedProvider, VMs: vms, StateDirectory: settings.StateDir}); err != nil {
		t.Fatal(err)
	}
	if restartedProvider.restoreCount != 1 {
		t.Fatalf("restart did not restore pending change: %d", restartedProvider.restoreCount)
	}
}

func TestValidateNetworkChangeRejectsUnsafeNames(t *testing.T) {
	for _, name := range []string{"", "lo", "bridge with spaces", "this-name-is-far-too-long"} {
		if err := ValidateChange(Change{Type: ChangeCreateBridge, BridgeName: name}); err == nil {
			t.Errorf("expected bridge name %q to be rejected", name)
		}
	}
}

func TestNetworkingStatusMapsManagedVMsToTheirBridges(t *testing.T) {
	vms, _, settings := vmtest.NewManager(t)
	if _, err := vms.Create(t.Context(), vmtest.BridgedOptions("bridge-workload", "br-lab")); err != nil {
		t.Fatal(err)
	}
	provider := &fakeNetworkProvider{status: Status{
		Bridges: []BridgeInfo{{Name: "br-lab", IsUp: true}},
	}}
	manager, err := NewManager(ManagerOptions{Provider: provider, VMs: vms, StateDirectory: settings.StateDir})
	if err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Bridges) != 1 || len(status.Bridges[0].Workloads) != 1 || status.Bridges[0].Workloads[0].Name != "bridge-workload" {
		t.Fatalf("managed workload was not mapped to its bridge: %#v", status.Bridges)
	}
}

func TestNetworkManagerRestoresMissingConfiguredBridge(t *testing.T) {
	vms, _, settings := vmtest.NewManager(t)
	if _, err := vms.Create(t.Context(), vmtest.BridgedOptions("bridge-workload", "br-lab")); err != nil {
		t.Fatal(err)
	}
	provider := &fakeNetworkProvider{}
	manager, err := NewManager(ManagerOptions{Provider: provider, VMs: vms, StateDirectory: settings.StateDir})
	if err != nil {
		t.Fatal(err)
	}
	manager.newID = func() (string, error) { return "restore-bridge", nil }

	status, err := manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Bridges) != 1 || len(status.Bridges[0].Diagnostics) != 1 || status.Bridges[0].Diagnostics[0].Key != "bridge_missing" {
		t.Fatalf("missing bridge was not exposed for repair: %#v", status.Bridges)
	}

	pending, err := manager.Apply(t.Context(), Change{Type: ChangeCreateBridge, BridgeName: "br-lab"})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.applied) != 1 || provider.applied[0].Type != ChangeCreateBridge || provider.applied[0].BridgeName != "br-lab" {
		t.Fatalf("missing bridge was not created: %#v", provider.applied)
	}
	if err := manager.Confirm(pending.ID); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkingStatusIncludesBridgeDHCPConfiguration(t *testing.T) {
	vms, _, settings := vmtest.NewManager(t)
	bridgeProvider := &fakeDHCPBridgeProvider{}
	dhcpService, err := dhcp.NewManager(settings.StateDir, bridgeProvider, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dhcpService.Configure("br-lab", true, false, dhcp.DefaultCIDR, false, dns.Config{}, ""); err != nil {
		t.Fatal(err)
	}
	provider := &fakeNetworkProvider{status: Status{Bridges: []BridgeInfo{{Name: "br-lab", IsUp: true}}}}
	manager, err := NewManager(ManagerOptions{Provider: provider, VMs: vms, DHCP: dhcpService, StateDirectory: settings.StateDir})
	if err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Bridges) != 1 || !status.Bridges[0].DHCP.Enabled || status.Bridges[0].DHCP.PoolStart != "192.168.100.50" {
		t.Fatalf("DHCP configuration was not included: %#v", status.Bridges)
	}
}

func TestNetworkingStatusLabelsDHCPLeasesWithVMNames(t *testing.T) {
	settings := config.Default(t.TempDir())
	vms := preloadedVMs(t, settings, vm.VirtualMachine{
		ID: "machine-id", Name: "web-01", NICs: []vm.NIC{{ID: "n0", Mode: vm.NetworkModeBridge, BridgeName: "br-lab", MACAddress: "52:54:00:44:81:f7"}},
	})
	dhcpService, err := dhcp.NewManager(settings.StateDir, &fakeDHCPBridgeProvider{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dhcpService.Configure("br-lab", true, false, dhcp.DefaultCIDR, false, dns.Config{}, ""); err != nil {
		t.Fatal(err)
	}
	if err := dhcpService.SetStaticLease("br-lab", "52:54:00:44:81:f7", "192.168.100.10"); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(ManagerOptions{Provider: &fakeNetworkProvider{status: Status{Bridges: []BridgeInfo{{Name: "br-lab"}}}}, VMs: vms, DHCP: dhcpService, StateDirectory: settings.StateDir})
	if err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Bridges[0].DHCP.Leases) != 1 || status.Bridges[0].DHCP.Leases[0].VMName != "web-01" {
		t.Fatalf("DHCP lease did not include the VM name: %#v", status.Bridges[0].DHCP.Leases)
	}
}

func TestNetworkingRejectsAutoDNSWhenLegacyVMNameIsInvalid(t *testing.T) {
	settings := config.Default(t.TempDir())
	vms := preloadedVMs(t, settings, vm.VirtualMachine{
		ID: "legacy", Name: "Legacy VM", Status: vm.StatusStopped,
		NICs: []vm.NIC{{ID: "n0", Mode: vm.NetworkModeBridge, BridgeName: "br0", MACAddress: "52:54:00:00:00:99"}},
	})
	bridgeProvider := &fakeDHCPBridgeProvider{}
	dhcpService, err := dhcp.NewManager(settings.StateDir, bridgeProvider, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(ManagerOptions{Provider: &fakeNetworkProvider{}, VMs: vms, DHCP: dhcpService, StateDirectory: settings.StateDir})
	if err != nil {
		t.Fatal(err)
	}
	err = manager.ConfigureBridgeSettings("br0", "", true, false, dhcp.DefaultCIDR, false, dns.Config{
		Enabled: true, Forwarders: []string{"1.1.1.1"}, Auto: true, Suffix: "br0.internal",
	}, "")
	if err == nil {
		t.Fatal("Auto DNS was enabled for a bridge with an invalid legacy VM name")
	}
}

func TestNetworkManagerReportsValidationAndLookupErrorsDistinctly(t *testing.T) {
	vms, _, settings := vmtest.NewManager(t)
	dhcpService, err := dhcp.NewManager(settings.StateDir, &fakeDHCPBridgeProvider{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeNetworkProvider{status: Status{Bridges: []BridgeInfo{{Name: "br0"}}}}
	manager, err := NewManager(ManagerOptions{Provider: provider, VMs: vms, DHCP: dhcpService, StateDirectory: settings.StateDir})
	if err != nil {
		t.Fatal(err)
	}
	var fieldErr *validate.FieldError
	if err := manager.ConfigureBridgeSettings("br0", "", true, false, "not-a-cidr", false, dns.Config{}, ""); !errors.As(err, &fieldErr) || fieldErr.Field != "cidr" {
		t.Fatalf("invalid CIDR = %v, want a cidr field error", err)
	}
	if err := manager.ConfigureBridgeSettings("missing", "", true, false, dhcp.DefaultCIDR, false, dns.Config{}, ""); !errors.Is(err, ErrBridgeNotFound) {
		t.Fatalf("missing bridge = %v, want ErrBridgeNotFound", err)
	}
	if err := manager.Confirm("no-such-change"); !errors.Is(err, ErrPendingChangeNotFound) {
		t.Fatalf("unknown change = %v, want ErrPendingChangeNotFound", err)
	}
	if _, err := manager.Leases("br0"); !errors.Is(err, ErrBridgeNotFound) {
		t.Fatalf("leases without DHCP = %v, want ErrBridgeNotFound", err)
	}
	if err := manager.ConfigureBridgeSettings("br0", "", true, false, dhcp.DefaultCIDR, false, dns.Config{}, ""); err != nil {
		t.Fatal(err)
	}
	if leases, err := manager.Leases("br0"); err != nil || len(leases) != 0 {
		t.Fatalf("leases = %#v, %v", leases, err)
	}
}

func TestNetworkManagerUpdatesBridgeDescriptionWithoutChangingServices(t *testing.T) {
	vms, _, settings := vmtest.NewManager(t)
	dhcpService, err := dhcp.NewManager(settings.StateDir, &fakeDHCPBridgeProvider{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeNetworkProvider{status: Status{Bridges: []BridgeInfo{{Name: "br0", Description: "Old description"}}}}
	manager, err := NewManager(ManagerOptions{Provider: provider, VMs: vms, DHCP: dhcpService, StateDirectory: settings.StateDir})
	if err != nil {
		t.Fatal(err)
	}
	current := dhcpService.Status("br0")
	if err := manager.ConfigureBridgeSettings("br0", "VM network", false, false, current.CIDR, false, dns.Config{}, ""); err != nil {
		t.Fatal(err)
	}
	if provider.descriptions["br0"] != "VM network" {
		t.Fatalf("bridge description was not updated: %#v", provider.descriptions)
	}
}

func TestNetworkManagerConfiguresGatewayOnlyBridge(t *testing.T) {
	vms, _, settings := vmtest.NewManager(t)
	dhcpService, err := dhcp.NewManager(settings.StateDir, &fakeDHCPBridgeProvider{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeNetworkProvider{status: Status{Bridges: []BridgeInfo{{Name: "prov0"}}}}
	manager, err := NewManager(ManagerOptions{Provider: provider, VMs: vms, DHCP: dhcpService, StateDirectory: settings.StateDir})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ConfigureBridgeSettings("prov0", "", false, true, "172.22.0.0/24", true, dns.Config{}, ""); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	bridge := status.Bridges[0].DHCP
	if bridge.Enabled || !bridge.GatewayOnly || !bridge.NATEnabled || bridge.ServerAddress != "172.22.0.1" {
		t.Fatalf("gateway-only mode was not reported: %#v", bridge)
	}
	if _, err := manager.Leases("prov0"); !errors.Is(err, ErrBridgeNotFound) {
		t.Fatalf("leases on a gateway-only bridge = %v, want ErrBridgeNotFound", err)
	}
	if bridgeServicesChanged(bridge, false, true, "172.22.0.0/24", true, dns.Config{}, "") {
		t.Fatal("unchanged gateway-only settings were reported as a change")
	}
	if !bridgeServicesChanged(bridge, true, false, "172.22.0.0/24", true, dns.Config{}, "") || !bridgeServicesChanged(bridge, false, true, "172.22.0.0/24", false, dns.Config{}, "") {
		t.Fatal("a change away from the gateway-only settings was not detected")
	}
	if err := manager.validateWorkloadSafety(Change{Type: ChangeDeleteBridge, BridgeName: "prov0"}); err == nil {
		t.Fatal("a gateway-only bridge could be deleted while configured")
	}
}

// preloadedVMs builds a VM manager whose store already holds definitions.
func preloadedVMs(t *testing.T, settings config.Config, machines ...vm.VirtualMachine) *vm.Manager {
	t.Helper()
	if err := config.PrepareDirectories(settings); err != nil {
		t.Fatal(err)
	}
	manager, err := vm.NewManager(vm.ManagerOptions{
		Settings: settings, Store: &vmtest.MemoryStore{VMs: machines}, Hypervisor: vmtest.NewFakeHypervisor(), Capabilities: vmtest.Capabilities,
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}
