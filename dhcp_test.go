package main

import (
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/server4"
)

type fakeDHCPBridgeProvider struct {
	validated []string
	ensured   []string
	err       error
}

func (p *fakeDHCPBridgeProvider) ValidateBridgeDHCP(name string, _ dhcpRange) error {
	p.validated = append(p.validated, name)
	return p.err
}

func (p *fakeDHCPBridgeProvider) EnsureBridgeDHCPAddress(name string, _ dhcpRange) error {
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
func (c *fakeBridgeNATController) Close() error          { return nil }

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
	network, err := parseDHCPRange("192.168.100.27/24")
	if err != nil {
		t.Fatal(err)
	}
	if network.Prefix.String() != "192.168.100.0/24" || network.Server.String() != "192.168.100.1" {
		t.Fatalf("unexpected normalized network: %#v", network)
	}
	if network.PoolStart.String() != "192.168.100.50" || network.PoolEnd.String() != "192.168.100.254" {
		t.Fatalf("unexpected DHCP pool: %s-%s", network.PoolStart, network.PoolEnd)
	}
	if _, err := parseDHCPRange("192.168.100.0/27"); err == nil {
		t.Fatal("a subnet too small to reach host offset 50 was accepted")
	}
}

func TestDHCPManagerPersistsLeasesAndHonoursRequestedAddresses(t *testing.T) {
	directory := t.TempDir()
	provider := &fakeDHCPBridgeProvider{}
	manager, err := newDHCPManager(directory, provider, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return time.Date(2026, 8, 20, 1, 0, 0, 0, time.UTC) }
	if err := manager.Configure("br0", true, defaultBridgeDHCPCIDR, false); err != nil {
		t.Fatal(err)
	}
	network, _ := parseDHCPRange(defaultBridgeDHCPCIDR)
	first, err := manager.acquireLease("br0", network, "mac:one", "52:54:00:00:00:01", netip.Addr{}, false)
	if err != nil || first.String() != "192.168.100.50" {
		t.Fatalf("unexpected first lease: %s, %v", first, err)
	}
	requested := netip.MustParseAddr("192.168.100.75")
	second, err := manager.acquireLease("br0", network, "mac:two", "52:54:00:00:00:02", requested, false)
	if err != nil || second != requested {
		t.Fatalf("requested address was not leased: %s, %v", second, err)
	}

	reloaded, err := newDHCPManager(directory, provider, nil)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.now = manager.now
	lease, err := reloaded.acquireLease("br0", network, "mac:one", "52:54:00:00:00:01", first, true)
	if err != nil || lease != first || reloaded.Status("br0").ActiveLeases != 2 {
		t.Fatalf("leases were not restored: %s, %v, %#v", lease, err, reloaded.Status("br0"))
	}
}

func TestDHCPManagerStartsOnceAndStopsAfterLastVM(t *testing.T) {
	provider := &fakeDHCPBridgeProvider{}
	manager, err := newDHCPManager(t.TempDir(), provider, nil)
	if err != nil {
		t.Fatal(err)
	}
	servers := make([]*fakeManagedDHCPServer, 0)
	manager.newServer = func(_ string, _ server4.Handler) (managedDHCPServer, error) {
		server := newFakeManagedDHCPServer()
		servers = append(servers, server)
		return server, nil
	}
	if err := manager.Configure("br0", true, defaultBridgeDHCPCIDR, false); err != nil {
		t.Fatal(err)
	}
	first := virtualMachine{ID: "one", NetworkMode: networkModeBridge, BridgeName: "br0"}
	second := virtualMachine{ID: "two", NetworkMode: networkModeBridge, BridgeName: "br0"}
	if err := manager.Prepare(first); err != nil {
		t.Fatal(err)
	}
	if err := manager.Prepare(second); err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 || len(provider.ensured) != 1 || !manager.Status("br0").Running {
		t.Fatalf("expected one running server: servers=%d ensured=%d status=%#v", len(servers), len(provider.ensured), manager.Status("br0"))
	}
	if err := manager.Configure("br0", false, "", false); err == nil {
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
	manager, err := newDHCPManager(t.TempDir(), provider, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return time.Date(2026, 8, 20, 1, 0, 0, 0, time.UTC) }
	var handler server4.Handler
	manager.newServer = func(_ string, supplied server4.Handler) (managedDHCPServer, error) {
		handler = supplied
		return newFakeManagedDHCPServer(), nil
	}
	if err := manager.Configure("br0", true, defaultBridgeDHCPCIDR, false); err != nil {
		t.Fatal(err)
	}
	vm := virtualMachine{ID: "one", NetworkMode: networkModeBridge, BridgeName: "br0"}
	if err := manager.Prepare(vm); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Release(vm) })
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

func TestDHCPNATStartsOnceAndSuppliesBridgeAsRouter(t *testing.T) {
	provider := &fakeDHCPBridgeProvider{}
	nat := newFakeBridgeNATController()
	manager, err := newDHCPManager(t.TempDir(), provider, nat)
	if err != nil {
		t.Fatal(err)
	}
	var handler server4.Handler
	manager.newServer = func(_ string, supplied server4.Handler) (managedDHCPServer, error) {
		handler = supplied
		return newFakeManagedDHCPServer(), nil
	}
	if err := manager.Configure("br0", true, defaultBridgeDHCPCIDR, true); err != nil {
		t.Fatal(err)
	}
	first := virtualMachine{ID: "one", NetworkMode: networkModeBridge, BridgeName: "br0"}
	second := virtualMachine{ID: "two", NetworkMode: networkModeBridge, BridgeName: "br0"}
	if err := manager.Prepare(first); err != nil {
		t.Fatal(err)
	}
	if err := manager.Prepare(second); err != nil {
		t.Fatal(err)
	}
	if len(nat.started) != 1 || !manager.Status("br0").NATRunning {
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
	if !nat.Running("br0") {
		t.Fatal("NAT stopped while a VM still used the bridge")
	}
	manager.Release(second)
	if nat.Running("br0") || len(nat.stopped) != 1 {
		t.Fatalf("NAT was not stopped with the last VM: running=%v stopped=%v", nat.Running("br0"), nat.stopped)
	}
}

func TestDHCPRejectsNATWithoutDHCP(t *testing.T) {
	manager, err := newDHCPManager(t.TempDir(), &fakeDHCPBridgeProvider{}, newFakeBridgeNATController())
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Configure("br0", false, "", true); err == nil {
		t.Fatal("NAT without managed DHCP was accepted")
	}
}

func TestDHCPManagerReportsBridgePreparationFailure(t *testing.T) {
	provider := &fakeDHCPBridgeProvider{err: errors.New("conflicting address")}
	manager, err := newDHCPManager(t.TempDir(), provider, nil)
	if err != nil {
		t.Fatal(err)
	}
	provider.err = nil
	if err := manager.Configure("br0", true, defaultBridgeDHCPCIDR, false); err != nil {
		t.Fatal(err)
	}
	provider.err = errors.New("conflicting address")
	err = manager.Prepare(virtualMachine{ID: "one", NetworkMode: networkModeBridge, BridgeName: "br0"})
	if err == nil || manager.Status("br0").LastError == "" {
		t.Fatalf("preparation failure was not reported: %v, %#v", err, manager.Status("br0"))
	}
}

func TestDHCPManagerSuggestsAndEnforcesDistinctBridgeRanges(t *testing.T) {
	provider := &fakeDHCPBridgeProvider{}
	manager, err := newDHCPManager(t.TempDir(), provider, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Configure("br0", true, "192.168.100.0/24", false); err != nil {
		t.Fatal(err)
	}
	if suggestion := manager.Status("br1").CIDR; suggestion != "192.168.101.0/24" {
		t.Fatalf("unexpected range suggestion for second bridge: %s", suggestion)
	}
	if err := manager.Configure("br1", true, "192.168.100.0/24", false); err == nil {
		t.Fatal("overlapping bridge DHCP ranges were accepted")
	}
}
