package dhcp

import (
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/server4"

	"github.com/roflware/app-runner/internal/dns"
	"github.com/roflware/app-runner/internal/testutil"
	"github.com/roflware/app-runner/internal/vm"
)

type fakeDHCPBridgeProvider struct {
	validated []string
	ensured   []string
	err       error
}

func (p *fakeDHCPBridgeProvider) ValidateBridgeDHCP(name string, _ Range) error {
	p.validated = append(p.validated, name)
	return p.err
}

func (p *fakeDHCPBridgeProvider) EnsureBridgeDHCPAddress(name string, _ Range) error {
	p.ensured = append(p.ensured, name)
	return p.err
}

type fakeManagedDHCPServer struct {
	closed chan struct{}
	once   sync.Once
}

type fakeBridgeNATController struct {
	started map[string]netip.Prefix
	stopped []string
}

func newFakeBridgeNATController() *fakeBridgeNATController {
	return &fakeBridgeNATController{started: make(map[string]netip.Prefix)}
}

func (c *fakeBridgeNATController) Start(bridge string, prefix netip.Prefix) error {
	c.started[bridge] = prefix
	return nil
}

func (c *fakeBridgeNATController) Stop(bridge string) error {
	delete(c.started, bridge)
	c.stopped = append(c.stopped, bridge)
	return nil
}

func (c *fakeBridgeNATController) Running(bridge string) bool {
	_, found := c.started[bridge]
	return found
}

func (c *fakeBridgeNATController) FinishRecovery() error { return nil }

type fakeBridgeDNSController struct {
	started map[string]dns.Config
	records map[string]dns.RecordProvider
	stopped []string
}

func newFakeBridgeDNSController() *fakeBridgeDNSController {
	return &fakeBridgeDNSController{started: make(map[string]dns.Config), records: make(map[string]dns.RecordProvider)}
}

func (c *fakeBridgeDNSController) Start(bridge string, _ netip.Addr, config dns.Config, records dns.RecordProvider) error {
	c.started[bridge] = config
	c.records[bridge] = records
	return nil
}

func (c *fakeBridgeDNSController) Stop(bridge string) error {
	delete(c.started, bridge)
	delete(c.records, bridge)
	c.stopped = append(c.stopped, bridge)
	return nil
}

func (c *fakeBridgeDNSController) Status(bridge string) (bool, string) {
	_, found := c.started[bridge]
	return found, ""
}

func (c *fakeBridgeDNSController) Close() error { return nil }

// bridgedTestVM builds a VM with a single bridged interface.
func bridgedTestVM(id, bridge, mac string) vm.VirtualMachine {
	return vm.VirtualMachine{ID: id, Name: id, NICs: []vm.NIC{{ID: id + "-nic", Mode: vm.NetworkModeBridge, BridgeName: bridge, MACAddress: mac}}}
}

func newFakeManagedDHCPServer() *fakeManagedDHCPServer {
	return &fakeManagedDHCPServer{closed: make(chan struct{})}
}

func (s *fakeManagedDHCPServer) Serve() error {
	<-s.closed
	return net.ErrClosed
}

func (s *fakeManagedDHCPServer) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

type recordingPacketConn struct {
	packet []byte
	peer   net.Addr
}

func (c *recordingPacketConn) ReadFrom([]byte) (int, net.Addr, error) { return 0, nil, net.ErrClosed }
func (c *recordingPacketConn) WriteTo(packet []byte, peer net.Addr) (int, error) {
	c.packet = append([]byte(nil), packet...)
	c.peer = peer
	return len(packet), nil
}
func (c *recordingPacketConn) Close() error                     { return nil }
func (c *recordingPacketConn) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (c *recordingPacketConn) SetDeadline(time.Time) error      { return nil }
func (c *recordingPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *recordingPacketConn) SetWriteDeadline(time.Time) error { return nil }

func TestParseDHCPRangeReservesStaticAddressesBeforeFifty(t *testing.T) {
	network, err := ParseRange("192.168.100.27/24")
	if err != nil {
		t.Fatal(err)
	}
	if network.Prefix.String() != "192.168.100.0/24" || network.Server.String() != "192.168.100.1" {
		t.Fatalf("unexpected normalized network: %#v", network)
	}
	if network.PoolStart.String() != "192.168.100.50" || network.PoolEnd.String() != "192.168.100.254" {
		t.Fatalf("unexpected DHCP pool: %s-%s", network.PoolStart, network.PoolEnd)
	}
	if _, err := ParseRange("192.168.100.0/27"); err == nil {
		t.Fatal("a subnet too small to reach host offset 50 was accepted")
	}
}

func TestDHCPManagerPersistsLeasesAndHonoursRequestedAddresses(t *testing.T) {
	directory := t.TempDir()
	provider := &fakeDHCPBridgeProvider{}
	manager, err := NewManager(directory, provider, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return time.Date(2026, 8, 20, 1, 0, 0, 0, time.UTC) }
	if err := manager.Configure("br0", true, DefaultCIDR, false, dns.Config{}); err != nil {
		t.Fatal(err)
	}
	network, _ := ParseRange(DefaultCIDR)
	first, err := manager.acquireLease("br0", network, "mac:one", "52:54:00:00:00:01", netip.Addr{}, false)
	if err != nil || first.String() != "192.168.100.50" {
		t.Fatalf("unexpected first lease: %s, %v", first, err)
	}
	requested := netip.MustParseAddr("192.168.100.75")
	second, err := manager.acquireLease("br0", network, "mac:two", "52:54:00:00:00:02", requested, false)
	if err != nil || second != requested {
		t.Fatalf("requested address was not leased: %s, %v", second, err)
	}

	reloaded, err := NewManager(directory, provider, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.now = manager.now
	lease, err := reloaded.acquireLease("br0", network, "mac:one", "52:54:00:00:00:01", first, true)
	if err != nil || lease != first || reloaded.Status("br0").ActiveLeases != 2 {
		t.Fatalf("leases were not restored: %s, %v, %#v", lease, err, reloaded.Status("br0"))
	}
}

func TestDHCPStatusListsOnlyActiveLeases(t *testing.T) {
	manager, err := NewManager(t.TempDir(), &fakeDHCPBridgeProvider{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 24, 1, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	if err := manager.Configure("br0", true, DefaultCIDR, false, dns.Config{}); err != nil {
		t.Fatal(err)
	}
	manager.state.Leases["br0"] = map[string]Lease{
		"active":  {HardwareAddress: "52:54:00:00:00:02", Address: "192.168.100.51", ExpiresAt: now.Add(time.Hour)},
		"expired": {HardwareAddress: "52:54:00:00:00:01", Address: "192.168.100.50", ExpiresAt: now.Add(-time.Hour)},
	}

	status := manager.Status("br0")
	if status.ActiveLeases != 1 || len(status.Leases) != 1 || status.Leases[0].Address != "192.168.100.51" {
		t.Fatalf("unexpected active leases: %#v", status)
	}
}

func TestDHCPManagerConsolidatesLeasesForTheSameHardwareAddress(t *testing.T) {
	manager, err := NewManager(t.TempDir(), &fakeDHCPBridgeProvider{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 24, 1, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	if err := manager.Configure("br0", true, DefaultCIDR, false, dns.Config{}); err != nil {
		t.Fatal(err)
	}
	manager.state.Leases["br0"] = map[string]Lease{
		"old-id": {ClientKey: "old-id", HardwareAddress: "52:54:00:44:81:f7", Address: "192.168.100.55", ExpiresAt: now.Add(time.Hour)},
		"new-id": {ClientKey: "new-id", HardwareAddress: "52:54:00:44:81:f7", Address: "192.168.100.56", ExpiresAt: now.Add(2 * time.Hour)},
	}
	network, _ := ParseRange(DefaultCIDR)

	address, err := manager.acquireLease("br0", network, "newest-id", "52:54:00:44:81:f7", netip.MustParseAddr("192.168.100.56"), true)
	if err != nil || address.String() != "192.168.100.56" {
		t.Fatalf("unexpected lease: %s, %v", address, err)
	}
	leases := manager.state.Leases["br0"]
	if len(leases) != 1 || leases["newest-id"].Address != "192.168.100.56" {
		t.Fatalf("duplicate MAC leases were not consolidated: %#v", leases)
	}
}

func TestDHCPManagerConsolidatesDuplicateHardwareAddressesOnLoad(t *testing.T) {
	directory := t.TempDir()
	manager, err := NewManager(directory, &fakeDHCPBridgeProvider{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 24, 1, 0, 0, 0, time.UTC)
	manager.state.Leases["br0"] = map[string]Lease{
		"older": {ClientKey: "older", HardwareAddress: "52:54:00:44:81:f7", Address: "192.168.100.55", ExpiresAt: now.Add(time.Hour)},
		"newer": {ClientKey: "newer", HardwareAddress: "52:54:00:44:81:f7", Address: "192.168.100.56", ExpiresAt: now.Add(2 * time.Hour)},
	}
	if err := manager.saveLocked(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewManager(directory, &fakeDHCPBridgeProvider{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	leases := reloaded.state.Leases["br0"]
	if len(leases) != 1 || leases["newer"].Address != "192.168.100.56" {
		t.Fatalf("duplicate MAC leases were not consolidated on load: %#v", leases)
	}
}

func TestDHCPManagerMovesLeaseToStaticAddress(t *testing.T) {
	manager, err := NewManager(t.TempDir(), &fakeDHCPBridgeProvider{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Configure("br0", true, DefaultCIDR, false, dns.Config{}); err != nil {
		t.Fatal(err)
	}
	manager.state.Leases["br0"] = map[string]Lease{
		"id:guest": {ClientKey: "id:guest", HardwareAddress: "52:54:00:44:81:f7", Address: "192.168.100.50", ExpiresAt: time.Now().Add(time.Hour)},
	}
	if err := manager.SetStaticLease("br0", "52:54:00:44:81:f7", "192.168.100.10"); err != nil {
		t.Fatal(err)
	}
	status := manager.Status("br0")
	if len(status.Leases) != 1 || !status.Leases[0].Static || status.Leases[0].Address != "192.168.100.10" {
		t.Fatalf("static reservation was not retained: %#v", status.Leases)
	}
	if err := manager.ClearDynamicLeases("br0"); err != nil {
		t.Fatal(err)
	}
	if len(manager.state.Leases["br0"]) != 1 {
		t.Fatalf("clearing dynamic leases removed the static reservation: %#v", manager.state.Leases)
	}
}

func TestDHCPManagerStartsOnceAndStopsAfterLastVM(t *testing.T) {
	provider := &fakeDHCPBridgeProvider{}
	manager, err := NewManager(t.TempDir(), provider, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	servers := make([]*fakeManagedDHCPServer, 0)
	manager.newServer = func(_ string, _ server4.Handler) (Server, error) {
		server := newFakeManagedDHCPServer()
		servers = append(servers, server)
		return server, nil
	}
	if err := manager.Configure("br0", true, DefaultCIDR, false, dns.Config{}); err != nil {
		t.Fatal(err)
	}
	first := bridgedTestVM("one", "br0", "52:54:00:00:00:11")
	second := bridgedTestVM("two", "br0", "52:54:00:00:00:12")
	if err := manager.Prepare(first); err != nil {
		t.Fatal(err)
	}
	if err := manager.Prepare(second); err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 || len(provider.ensured) != 1 || !manager.Status("br0").Running {
		t.Fatalf("expected one running server: servers=%d ensured=%d status=%#v", len(servers), len(provider.ensured), manager.Status("br0"))
	}
	if err := manager.Configure("br0", false, "", false, dns.Config{}); err == nil {
		t.Fatal("DHCP configuration changed while VMs were active")
	}
	manager.Release(first)
	if !manager.Status("br0").Running {
		t.Fatal("server stopped while a VM still used the bridge")
	}
	manager.Release(second)
	if manager.Status("br0").Running {
		t.Fatal("server remained running after the last VM stopped")
	}
	select {
	case <-servers[0].closed:
	default:
		t.Fatal("server listener was not closed")
	}
}

func TestDHCPHandlerOffersFirstPoolAddress(t *testing.T) {
	provider := &fakeDHCPBridgeProvider{}
	manager, err := NewManager(t.TempDir(), provider, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return time.Date(2026, 8, 20, 1, 0, 0, 0, time.UTC) }
	var handler server4.Handler
	manager.newServer = func(_ string, supplied server4.Handler) (Server, error) {
		handler = supplied
		return newFakeManagedDHCPServer(), nil
	}
	if err := manager.Configure("br0", true, DefaultCIDR, false, dns.Config{}); err != nil {
		t.Fatal(err)
	}
	machine := bridgedTestVM("one", "br0", "52:54:00:00:00:11")
	if err := manager.Prepare(machine); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Release(machine) })
	discover, err := dhcpv4.NewDiscovery(net.HardwareAddr{0x52, 0x54, 0x00, 0, 0, 1})
	if err != nil {
		t.Fatal(err)
	}
	connection := &recordingPacketConn{}
	peer := &net.UDPAddr{IP: net.IPv4bcast, Port: dhcpv4.ClientPort}
	handler(connection, peer, discover)
	if len(connection.packet) == 0 {
		t.Fatal("DHCP handler did not reply")
	}
	offer, err := dhcpv4.FromBytes(connection.packet)
	if err != nil {
		t.Fatal(err)
	}
	if offer.MessageType() != dhcpv4.MessageTypeOffer || offer.YourIPAddr.String() != "192.168.100.50" {
		t.Fatalf("unexpected offer: %s", offer.Summary())
	}
	if !offer.ServerIdentifier().Equal(net.ParseIP("192.168.100.1")) {
		t.Fatalf("unexpected server identifier: %s", offer.ServerIdentifier())
	}
	if routers := offer.Router(); len(routers) != 0 {
		t.Fatalf("non-NAT DHCP offer unexpectedly supplied routers: %v", routers)
	}

	conflictingRequest, err := dhcpv4.New(
		dhcpv4.WithMessageType(dhcpv4.MessageTypeRequest),
		dhcpv4.WithHwAddr(net.HardwareAddr{0x52, 0x54, 0x00, 0, 0, 2}),
		dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(net.ParseIP("192.168.100.50"))),
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(net.ParseIP("192.168.100.1"))),
	)
	if err != nil {
		t.Fatal(err)
	}
	connection.packet = nil
	handler(connection, peer, conflictingRequest)
	nak, err := dhcpv4.FromBytes(connection.packet)
	if err != nil {
		t.Fatal(err)
	}
	if nak.MessageType() != dhcpv4.MessageTypeNak {
		t.Fatalf("conflicting request was not rejected: %s", nak.Summary())
	}
}

func TestDHCPHandlerLogsRequestsAndResponses(t *testing.T) {
	logs := testutil.CaptureLogs(t)
	manager, err := NewManager(t.TempDir(), &fakeDHCPBridgeProvider{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return time.Date(2026, 8, 20, 1, 0, 0, 0, time.UTC) }
	network, err := ParseRange(DefaultCIDR)
	if err != nil {
		t.Fatal(err)
	}
	discover, err := dhcpv4.NewDiscovery(net.HardwareAddr{0x52, 0x54, 0x00, 0, 0, 1})
	if err != nil {
		t.Fatal(err)
	}
	connection := &recordingPacketConn{}
	manager.handlePacket(
		"br0",
		network,
		BridgeConfig{Enabled: true, CIDR: DefaultCIDR},
		connection,
		&net.UDPAddr{IP: net.IPv4bcast, Port: dhcpv4.ClientPort},
		discover,
	)

	output := logs.String()
	for _, expected := range []string{
		`"msg":"DHCP request received"`,
		`"msg":"DHCP response sent"`,
		`"bridge":"br0"`,
		`"client":"52:54:00:00:00:01"`,
		`"message_type":"DISCOVER"`,
		`"message_type":"OFFER"`,
		`"assigned_address":"192.168.100.50"`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("DHCP logs do not contain %s: %s", expected, output)
		}
	}
}

func TestDHCPNATStartsOnceAndSuppliesBridgeAsRouter(t *testing.T) {
	provider := &fakeDHCPBridgeProvider{}
	natController := newFakeBridgeNATController()
	manager, err := NewManager(t.TempDir(), provider, natController, nil)
	if err != nil {
		t.Fatal(err)
	}
	var handler server4.Handler
	manager.newServer = func(_ string, supplied server4.Handler) (Server, error) {
		handler = supplied
		return newFakeManagedDHCPServer(), nil
	}
	if err := manager.Configure("br0", true, DefaultCIDR, true, dns.Config{}); err != nil {
		t.Fatal(err)
	}
	first := bridgedTestVM("one", "br0", "52:54:00:00:00:11")
	second := bridgedTestVM("two", "br0", "52:54:00:00:00:12")
	if err := manager.Prepare(first); err != nil {
		t.Fatal(err)
	}
	if err := manager.Prepare(second); err != nil {
		t.Fatal(err)
	}
	if len(natController.started) != 1 || !manager.Status("br0").NATRunning {
		t.Fatalf("NAT did not share the DHCP lifecycle: %#v", manager.Status("br0"))
	}
	discover, err := dhcpv4.NewDiscovery(net.HardwareAddr{0x52, 0x54, 0x00, 0, 0, 3})
	if err != nil {
		t.Fatal(err)
	}
	connection := &recordingPacketConn{}
	handler(connection, &net.UDPAddr{IP: net.IPv4bcast, Port: dhcpv4.ClientPort}, discover)
	offer, err := dhcpv4.FromBytes(connection.packet)
	if err != nil {
		t.Fatal(err)
	}
	if routers := offer.Router(); len(routers) != 1 || !routers[0].Equal(net.ParseIP("192.168.100.1")) {
		t.Fatalf("NAT offer did not advertise the bridge address as router: %v", routers)
	}
	manager.Release(first)
	if !natController.Running("br0") {
		t.Fatal("NAT stopped while a VM still used the bridge")
	}
	manager.Release(second)
	if natController.Running("br0") || len(natController.stopped) != 1 {
		t.Fatalf("NAT was not stopped with the last VM: running=%v stopped=%v", natController.Running("br0"), natController.stopped)
	}
}

func TestDHCPRejectsNATWithoutDHCP(t *testing.T) {
	manager, err := NewManager(t.TempDir(), &fakeDHCPBridgeProvider{}, newFakeBridgeNATController(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Configure("br0", false, "", true, dns.Config{}); err == nil {
		t.Fatal("NAT without managed DHCP was accepted")
	}
}

func TestDHCPDNSLifecycleOptionsAndAutomaticRecords(t *testing.T) {
	provider := &fakeDHCPBridgeProvider{}
	dnsController := newFakeBridgeDNSController()
	manager, err := NewManager(t.TempDir(), provider, nil, dnsController)
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return time.Date(2026, 8, 20, 1, 0, 0, 0, time.UTC) }
	var handler server4.Handler
	manager.newServer = func(_ string, supplied server4.Handler) (Server, error) {
		handler = supplied
		return newFakeManagedDHCPServer(), nil
	}
	config := dns.Config{Enabled: true, Forwarders: []string{"1.1.1.1"}, Auto: true, Suffix: "br0.internal"}
	if err := manager.Configure("br0", true, DefaultCIDR, false, config); err != nil {
		t.Fatal(err)
	}
	machine := bridgedTestVM("one", "br0", "52:54:00:00:00:04")
	machine.Name = "web-1"
	if err := manager.Prepare(machine); err != nil {
		t.Fatal(err)
	}
	discover, err := dhcpv4.NewDiscovery(net.HardwareAddr{0x52, 0x54, 0x00, 0, 0, 4})
	if err != nil {
		t.Fatal(err)
	}
	connection := &recordingPacketConn{}
	handler(connection, &net.UDPAddr{IP: net.IPv4bcast, Port: dhcpv4.ClientPort}, discover)
	offer, err := dhcpv4.FromBytes(connection.packet)
	if err != nil {
		t.Fatal(err)
	}
	if servers := offer.DNS(); len(servers) != 1 || !servers[0].Equal(net.ParseIP("192.168.100.1")) {
		t.Fatalf("managed DNS address was not advertised: %v", servers)
	}
	if suffix := offer.DomainName(); suffix != "br0.internal" {
		t.Fatalf("Auto DNS suffix was not advertised: %q", suffix)
	}
	records := dnsController.records["br0"]()
	if address := records["web-1"]; address.String() != "192.168.100.50" {
		t.Fatalf("leased VM was not published to Auto DNS: %v", records)
	}
	manager.Release(machine)
	if running, _ := dnsController.Status("br0"); running || len(dnsController.stopped) != 1 {
		t.Fatalf("DNS did not stop after the last VM: running=%v stopped=%v", running, dnsController.stopped)
	}
}

func TestDHCPManagerPersistsDNSConfiguration(t *testing.T) {
	directory := t.TempDir()
	provider := &fakeDHCPBridgeProvider{}
	manager, err := NewManager(directory, provider, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	config := dns.Config{Enabled: true, Forwarders: []string{"1.1.1.1", "9.9.9.9"}, Auto: true, Suffix: "BR0.Internal."}
	if err := manager.Configure("br0", true, DefaultCIDR, false, config); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewManager(directory, provider, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	status := reloaded.Status("br0")
	if !status.DNSEnabled || !status.AutoDNS || status.DNSSuffix != "br0.internal" || len(status.DNSForwarders) != 2 {
		t.Fatalf("DNS configuration was not restored: %#v", status)
	}
}

func TestDHCPRollsBackDNSWhenDHCPListenerCannotStart(t *testing.T) {
	dnsController := newFakeBridgeDNSController()
	manager, err := NewManager(t.TempDir(), &fakeDHCPBridgeProvider{}, nil, dnsController)
	if err != nil {
		t.Fatal(err)
	}
	manager.newServer = func(string, server4.Handler) (Server, error) {
		return nil, errors.New("port 67 unavailable")
	}
	if err := manager.Configure("br0", true, DefaultCIDR, false, dns.Config{
		Enabled: true, Forwarders: []string{"1.1.1.1"},
	}); err != nil {
		t.Fatal(err)
	}
	err = manager.Prepare(bridgedTestVM("one", "br0", "52:54:00:00:00:21"))
	if err == nil {
		t.Fatal("DHCP listener failure was not reported")
	}
	if running, _ := dnsController.Status("br0"); running || len(dnsController.stopped) != 1 {
		t.Fatalf("DNS listener was not rolled back: running=%v stopped=%v", running, dnsController.stopped)
	}
}

func TestDHCPManagerReportsBridgePreparationFailure(t *testing.T) {
	provider := &fakeDHCPBridgeProvider{err: errors.New("conflicting address")}
	manager, err := NewManager(t.TempDir(), provider, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	provider.err = nil
	if err := manager.Configure("br0", true, DefaultCIDR, false, dns.Config{}); err != nil {
		t.Fatal(err)
	}
	provider.err = errors.New("conflicting address")
	err = manager.Prepare(bridgedTestVM("one", "br0", "52:54:00:00:00:11"))
	if err == nil || manager.Status("br0").LastError == "" {
		t.Fatalf("preparation failure was not reported: %v, %#v", err, manager.Status("br0"))
	}
}

func TestDHCPManagerSuggestsAndEnforcesDistinctBridgeRanges(t *testing.T) {
	provider := &fakeDHCPBridgeProvider{}
	manager, err := NewManager(t.TempDir(), provider, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Configure("br0", true, "192.168.100.0/24", false, dns.Config{}); err != nil {
		t.Fatal(err)
	}
	if suggestion := manager.Status("br1").CIDR; suggestion != "192.168.101.0/24" {
		t.Fatalf("unexpected range suggestion for second bridge: %s", suggestion)
	}
	if err := manager.Configure("br1", true, "192.168.100.0/24", false, dns.Config{}); err == nil {
		t.Fatal("overlapping bridge DHCP ranges were accepted")
	}
}

func TestDHCPManagerPreparesEveryBridgeAVMUses(t *testing.T) {
	provider := &fakeDHCPBridgeProvider{}
	manager, err := NewManager(t.TempDir(), provider, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.newServer = func(string, server4.Handler) (Server, error) { return newFakeManagedDHCPServer(), nil }
	if err := manager.Configure("br0", true, "192.168.100.0/24", false, dns.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Configure("br1", true, "192.168.101.0/24", false, dns.Config{}); err != nil {
		t.Fatal(err)
	}
	machine := vm.VirtualMachine{ID: "dual", Name: "dual", NICs: []vm.NIC{
		{ID: "a", Mode: vm.NetworkModeBridge, BridgeName: "br0", MACAddress: "52:54:00:00:00:31"},
		{ID: "b", Mode: vm.NetworkModeBridge, BridgeName: "br1", MACAddress: "52:54:00:00:00:32"},
		{ID: "c", Mode: vm.NetworkModeNAT, MACAddress: "52:54:00:00:00:33"},
	}}
	if err := manager.Prepare(machine); err != nil {
		t.Fatal(err)
	}
	if !manager.Status("br0").Running || !manager.Status("br1").Running {
		t.Fatalf("both bridges should be serving: br0=%v br1=%v", manager.Status("br0").Running, manager.Status("br1").Running)
	}
	manager.Release(machine)
	if manager.Status("br0").Running || manager.Status("br1").Running {
		t.Fatal("bridges kept running after the VM was released")
	}
}

func TestDHCPManagerRollsBackEarlierBridgesWhenALaterOneFails(t *testing.T) {
	provider := &fakeDHCPBridgeProvider{}
	manager, err := NewManager(t.TempDir(), provider, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.newServer = func(bridge string, _ server4.Handler) (Server, error) {
		if bridge == "br1" {
			return nil, errors.New("port 67 unavailable on br1")
		}
		return newFakeManagedDHCPServer(), nil
	}
	if err := manager.Configure("br0", true, "192.168.100.0/24", false, dns.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Configure("br1", true, "192.168.101.0/24", false, dns.Config{}); err != nil {
		t.Fatal(err)
	}
	machine := vm.VirtualMachine{ID: "dual", Name: "dual", NICs: []vm.NIC{
		{ID: "a", Mode: vm.NetworkModeBridge, BridgeName: "br0", MACAddress: "52:54:00:00:00:41"},
		{ID: "b", Mode: vm.NetworkModeBridge, BridgeName: "br1", MACAddress: "52:54:00:00:00:42"},
	}}
	if err := manager.Prepare(machine); err == nil {
		t.Fatal("failure on the second bridge was not reported")
	}
	if manager.Status("br0").Running {
		t.Fatal("first bridge was left running after the second failed")
	}
}

func TestDHCPShutdownClosesListenersButKeepsNAT(t *testing.T) {
	provider := &fakeDHCPBridgeProvider{}
	natController := newFakeBridgeNATController()
	manager, err := NewManager(t.TempDir(), provider, natController, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := newFakeManagedDHCPServer()
	manager.newServer = func(string, server4.Handler) (Server, error) { return server, nil }
	if err := manager.Configure("br0", true, DefaultCIDR, true, dns.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Prepare(bridgedTestVM("one", "br0", "52:54:00:00:00:51")); err != nil {
		t.Fatal(err)
	}
	if err := manager.Shutdown(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.closed:
	default:
		t.Fatal("DHCP listener was not closed on shutdown")
	}
	if !natController.Running("br0") || len(natController.stopped) != 0 {
		t.Fatalf("shutdown tore down NAT that running guests still need: running=%v stopped=%v", natController.Running("br0"), natController.stopped)
	}
}

func TestDHCPAddressesListsActiveLeasesForAMAC(t *testing.T) {
	manager, err := NewManager(t.TempDir(), &fakeDHCPBridgeProvider{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 24, 1, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	manager.state.Leases["br0"] = map[string]Lease{
		"active":  {HardwareAddress: "52:54:00:00:00:02", Address: "192.168.100.51", ExpiresAt: now.Add(time.Hour)},
		"expired": {HardwareAddress: "52:54:00:00:00:02", Address: "192.168.100.52", ExpiresAt: now.Add(-time.Hour)},
		"other":   {HardwareAddress: "52:54:00:00:00:03", Address: "192.168.100.53", ExpiresAt: now.Add(time.Hour)},
	}
	if got := manager.Addresses("br0", "52:54:00:00:00:02"); len(got) != 1 || got[0] != "192.168.100.51" {
		t.Fatalf("Addresses = %v", got)
	}
	if got := manager.Addresses("br1", "52:54:00:00:00:02"); len(got) != 0 {
		t.Fatalf("Addresses on another bridge = %v", got)
	}
}

func TestDHCPAddressConflicts(t *testing.T) {
	network, err := ParseRange("192.168.100.0/24")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		onBridge bool
		existing string
		want     bool
	}{
		{"unrelated subnet elsewhere", false, "10.0.0.1/8", false},
		{"unrelated subnet on the bridge", true, "10.0.0.1/8", false},
		{"overlapping subnet on another interface", false, "192.168.100.7/24", true},
		{"wider subnet on another interface", false, "192.168.0.1/16", true},
		{"server address on the bridge", true, "192.168.100.1/24", false},
		{"server address with another prefix length", true, "192.168.100.1/25", true},
		{"virtual BMC address below the pool on the bridge", true, "192.168.100.20/24", false},
		{"address inside the pool on the bridge", true, "192.168.100.50/24", true},
		{"pool end on the bridge", true, "192.168.100.254/24", true},
		{"wider subnet on the bridge", true, "192.168.0.1/16", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := AddressConflicts(network, test.onBridge, netip.MustParsePrefix(test.existing)); got != test.want {
				t.Errorf("AddressConflicts(%s, onBridge=%v) = %v, want %v", test.existing, test.onBridge, got, test.want)
			}
		})
	}
}
