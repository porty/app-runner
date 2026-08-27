package apprunner

import (
	"os"
	"testing"
	"time"
)

type fakeNetworkProvider struct {
	status       networkingStatus
	snapshot     networkSnapshot
	applied      []networkChange
	restoreCount int
	restored     chan struct{}
	descriptions map[string]string
}

func (p *fakeNetworkProvider) Inspect() (networkingStatus, error) {
	return p.status, nil
}

func (p *fakeNetworkProvider) Snapshot(change networkChange) (networkSnapshot, error) {
	p.snapshot.Change = change
	return p.snapshot, nil
}

func (p *fakeNetworkProvider) Apply(change networkChange) error {
	p.applied = append(p.applied, change)
	return nil
}

func (p *fakeNetworkProvider) Restore(networkSnapshot) error {
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
	vms, _, settings := newTestVMManager(t)
	provider := &fakeNetworkProvider{}
	manager, err := newNetworkManager(networkManagerOptions{Provider: provider, VMs: vms, StateDirectory: settings.StateDir})
	if err != nil {
		t.Fatal(err)
	}
	manager.confirmationWindow = 30 * time.Millisecond
	manager.newID = func() (string, error) { return "change-id", nil }

	pending, err := manager.Apply(t.Context(), networkChange{Type: networkChangeCreateBridge, BridgeName: "br1"})
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
	vms, _, settings := newTestVMManager(t)
	provider := &fakeNetworkProvider{restored: make(chan struct{}, 1)}
	manager, err := newNetworkManager(networkManagerOptions{Provider: provider, VMs: vms, StateDirectory: settings.StateDir})
	if err != nil {
		t.Fatal(err)
	}
	manager.confirmationWindow = 20 * time.Millisecond
	manager.newID = func() (string, error) { return "change-id", nil }
	if _, err := manager.Apply(t.Context(), networkChange{Type: networkChangeSetBridgeUp, BridgeName: "br1"}); err != nil {
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
	vms, _, settings := newTestVMManager(t)
	provider := &fakeNetworkProvider{}
	manager, err := newNetworkManager(networkManagerOptions{Provider: provider, VMs: vms, StateDirectory: settings.StateDir})
	if err != nil {
		t.Fatal(err)
	}
	manager.confirmationWindow = time.Hour
	manager.newID = func() (string, error) { return "change-id", nil }
	if _, err := manager.Apply(t.Context(), networkChange{Type: networkChangeCreateBridge, BridgeName: "br1"}); err != nil {
		t.Fatal(err)
	}
	manager.timer.Stop()

	restartedProvider := &fakeNetworkProvider{}
	if _, err := newNetworkManager(networkManagerOptions{Provider: restartedProvider, VMs: vms, StateDirectory: settings.StateDir}); err != nil {
		t.Fatal(err)
	}
	if restartedProvider.restoreCount != 1 {
		t.Fatalf("restart did not restore pending change: %d", restartedProvider.restoreCount)
	}
}

func TestValidateNetworkChangeRejectsUnsafeNames(t *testing.T) {
	for _, name := range []string{"", "lo", "bridge with spaces", "this-name-is-far-too-long"} {
		if err := validateNetworkChange(networkChange{Type: networkChangeCreateBridge, BridgeName: name}); err == nil {
			t.Errorf("expected bridge name %q to be rejected", name)
		}
	}
}

func TestNetworkingStatusMapsManagedVMsToTheirBridges(t *testing.T) {
	vms, _, settings := newTestVMManager(t)
	if _, err := vms.Create(t.Context(), bridgedVMOptions("bridge-workload", "br-lab")); err != nil {
		t.Fatal(err)
	}
	provider := &fakeNetworkProvider{status: networkingStatus{
		Bridges: []networkBridgeInfo{{Name: "br-lab", IsUp: true}},
	}}
	manager, err := newNetworkManager(networkManagerOptions{Provider: provider, VMs: vms, StateDirectory: settings.StateDir})
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
	vms, _, settings := newTestVMManager(t)
	if _, err := vms.Create(t.Context(), bridgedVMOptions("bridge-workload", "br-lab")); err != nil {
		t.Fatal(err)
	}
	provider := &fakeNetworkProvider{}
	manager, err := newNetworkManager(networkManagerOptions{Provider: provider, VMs: vms, StateDirectory: settings.StateDir})
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

	pending, err := manager.Apply(t.Context(), networkChange{Type: networkChangeCreateBridge, BridgeName: "br-lab"})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.applied) != 1 || provider.applied[0].Type != networkChangeCreateBridge || provider.applied[0].BridgeName != "br-lab" {
		t.Fatalf("missing bridge was not created: %#v", provider.applied)
	}
	if err := manager.Confirm(pending.ID); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkingStatusIncludesBridgeDHCPConfiguration(t *testing.T) {
	vms, _, settings := newTestVMManager(t)
	bridgeProvider := &fakeDHCPBridgeProvider{}
	dhcp, err := newDHCPManager(settings.StateDir, bridgeProvider, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dhcp.Configure("br-lab", true, defaultBridgeDHCPCIDR, false, bridgeDNSConfig{}); err != nil {
		t.Fatal(err)
	}
	provider := &fakeNetworkProvider{status: networkingStatus{Bridges: []networkBridgeInfo{{Name: "br-lab", IsUp: true}}}}
	manager, err := newNetworkManager(networkManagerOptions{Provider: provider, VMs: vms, DHCP: dhcp, StateDirectory: settings.StateDir})
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
	vms, _, settings := newTestVMManager(t)
	vms.vms = append(vms.vms, virtualMachine{
		ID: "vm-id", Name: "web-01", NICs: []vmNIC{{ID: "n0", Mode: networkModeBridge, BridgeName: "br-lab", MACAddress: "52:54:00:44:81:f7"}},
	})
	dhcp, err := newDHCPManager(settings.StateDir, &fakeDHCPBridgeProvider{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dhcp.Configure("br-lab", true, defaultBridgeDHCPCIDR, false, bridgeDNSConfig{}); err != nil {
		t.Fatal(err)
	}
	dhcp.state.Leases["br-lab"] = map[string]dhcpLease{
		"id:guest": {ClientKey: "id:guest", HardwareAddress: "52:54:00:44:81:f7", Address: "192.168.100.50", ExpiresAt: time.Now().Add(time.Hour)},
	}
	manager, err := newNetworkManager(networkManagerOptions{Provider: &fakeNetworkProvider{status: networkingStatus{Bridges: []networkBridgeInfo{{Name: "br-lab"}}}}, VMs: vms, DHCP: dhcp, StateDirectory: settings.StateDir})
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
	vms, _, settings := newTestVMManager(t)
	vms.vms = append(vms.vms, virtualMachine{
		ID: "legacy", Name: "Legacy VM", Status: vmStatusStopped,
		NICs: []vmNIC{{ID: "n0", Mode: networkModeBridge, BridgeName: "br0", MACAddress: "52:54:00:00:00:99"}},
	})
	bridgeProvider := &fakeDHCPBridgeProvider{}
	dhcp, err := newDHCPManager(settings.StateDir, bridgeProvider, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newNetworkManager(networkManagerOptions{Provider: &fakeNetworkProvider{}, VMs: vms, DHCP: dhcp, StateDirectory: settings.StateDir})
	if err != nil {
		t.Fatal(err)
	}
	err = manager.ConfigureBridgeSettings("br0", "", true, defaultBridgeDHCPCIDR, false, bridgeDNSConfig{
		Enabled: true, Forwarders: []string{"1.1.1.1"}, Auto: true, Suffix: "br0.internal",
	})
	if err == nil {
		t.Fatal("Auto DNS was enabled for a bridge with an invalid legacy VM name")
	}
}

func TestNetworkManagerUpdatesBridgeDescriptionWithoutChangingServices(t *testing.T) {
	vms, _, settings := newTestVMManager(t)
	dhcp, err := newDHCPManager(settings.StateDir, &fakeDHCPBridgeProvider{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeNetworkProvider{status: networkingStatus{Bridges: []networkBridgeInfo{{Name: "br0", Description: "Old description"}}}}
	manager, err := newNetworkManager(networkManagerOptions{Provider: provider, VMs: vms, DHCP: dhcp, StateDirectory: settings.StateDir})
	if err != nil {
		t.Fatal(err)
	}
	current := dhcp.Status("br0")
	if err := manager.ConfigureBridgeSettings("br0", "VM network", false, current.CIDR, false, bridgeDNSConfig{}); err != nil {
		t.Fatal(err)
	}
	if provider.descriptions["br0"] != "VM network" {
		t.Fatalf("bridge description was not updated: %#v", provider.descriptions)
	}
}
