// Package dhcp runs App Runner's per-bridge DHCPv4 service and
// orchestrates the NAT and DNS services that share its lifecycle.
package dhcp

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/server4"

	"github.com/roflware/app-runner/internal/dns"
	"github.com/roflware/app-runner/internal/fsutil"
	"github.com/roflware/app-runner/internal/validate"
	"github.com/roflware/app-runner/internal/vm"
)

var ErrLeaseNotFound = errors.New("DHCP lease not found")

const (
	DefaultCIDR     = "192.168.100.0/24"
	poolStartOffset = uint32(50)
	leaseDuration   = 24 * time.Hour
	httpBootClass   = "HTTPClient"
)

// BridgeConfig is the persisted guest network configuration of a bridge.
// Enabled means App Runner manages the bridge's subnet at all; GatewayOnly
// narrows that to assigning the gateway address and optional NAT while an
// external DHCP server, such as Ironic's dnsmasq, serves the bridge.
type BridgeConfig struct {
	Enabled     bool       `json:"enabled"`
	GatewayOnly bool       `json:"gateway_only,omitempty"`
	CIDR        string     `json:"cidr"`
	NAT         bool       `json:"nat,omitempty"`
	DNS         dns.Config `json:"dns,omitempty"`
	HTTPBootURL string     `json:"http_boot_url,omitempty"`
}

type Lease struct {
	ClientKey       string    `json:"client_key"`
	HardwareAddress string    `json:"hardware_address,omitempty"`
	Address         string    `json:"address"`
	ExpiresAt       time.Time `json:"expires_at"`
	Static          bool      `json:"static,omitempty"`
	VMName          string    `json:"-"`
}

type persistedState struct {
	Bridges map[string]BridgeConfig     `json:"bridges"`
	Leases  map[string]map[string]Lease `json:"leases,omitempty"`
}

// BridgeStatus reports a bridge's guest network services. Enabled means App
// Runner serves DHCP; GatewayOnly means it only assigns ServerAddress as the
// gateway (with optional NAT) and leaves DHCP to another server.
type BridgeStatus struct {
	Enabled       bool
	GatewayOnly   bool
	CIDR          string
	ServerAddress string
	PoolStart     string
	PoolEnd       string
	Running       bool
	ActiveLeases  uint32
	LastError     string
	NATEnabled    bool
	NATRunning    bool
	DNSEnabled    bool
	DNSForwarders []string
	AutoDNS       bool
	DNSSuffix     string
	DNSRunning    bool
	HTTPBootURL   string
	Leases        []Lease
}

// Range describes a managed subnet. Gateway-only subnets have no lease pool,
// so PoolStart and PoolEnd are the zero Addr.
type Range struct {
	Prefix    netip.Prefix
	Server    netip.Addr
	PoolStart netip.Addr
	PoolEnd   netip.Addr
	Broadcast netip.Addr
}

func (c BridgeConfig) network() (Range, error) {
	if c.GatewayOnly {
		return ParseGatewayRange(c.CIDR)
	}
	return ParseRange(c.CIDR)
}

type BridgeProvider interface {
	ValidateBridgeDHCP(string, Range) error
	EnsureBridgeDHCPAddress(string, Range) error
}

type Server interface {
	Serve() error
	Close() error
}

type serverFactory func(string, server4.Handler) (Server, error)

type bridgeRuntime struct {
	server  Server
	users   map[string]struct{}
	running bool
	nat     bool
	dns     bool
	vms     map[string]vm.VirtualMachine
}

type Manager struct {
	mu                sync.Mutex
	path              string
	provider          BridgeProvider
	newServer         serverFactory
	nat               NATController
	dns               DNSController
	now               func() time.Time
	state             persistedState
	runtimes          map[string]*bridgeRuntime
	errors            map[string]string
	defaultForwarders []string
}

// NATController and DNSController are the guest services that follow the
// DHCP lifecycle of a bridge; the nat and dns packages implement them.
type NATController interface {
	Start(string, netip.Prefix) error
	Stop(string) error
	Running(string) bool
	FinishRecovery() error
}

type DNSController interface {
	Start(string, netip.Addr, dns.Config, dns.RecordProvider) error
	Stop(string) error
	Status(string) (bool, string)
	Close() error
}

func NewManager(diskDirectory string, provider BridgeProvider, natController NATController, dnsController DNSController) (*Manager, error) {
	manager := &Manager{
		path: filepath.Join(diskDirectory, "dhcp.json"), provider: provider,
		newServer: newInProcessServer, now: time.Now,
		state:    persistedState{Bridges: make(map[string]BridgeConfig), Leases: make(map[string]map[string]Lease)},
		runtimes: make(map[string]*bridgeRuntime), errors: make(map[string]string),
		defaultForwarders: dns.DefaultForwarders(),
	}
	manager.nat = natController
	manager.dns = dnsController
	if err := manager.load(); err != nil {
		return nil, err
	}
	return manager, nil
}

func newInProcessServer(bridge string, handler server4.Handler) (Server, error) {
	return server4.NewServer(bridge, &net.UDPAddr{IP: net.IPv4zero, Port: dhcpv4.ServerPort}, handler)
}

// Configure sets a bridge's guest network services. enabled runs App
// Runner's DHCP server on the bridge; gatewayOnly instead assigns only the
// subnet's gateway address (and NAT when requested) for an external DHCP
// server to use. Disabling both removes the bridge's configuration.
func (m *Manager) Configure(bridge string, enabled, gatewayOnly bool, cidr string, natEnabled bool, dnsConfig dns.Config, httpBootURL string) error {
	if err := validate.InterfaceName(bridge); err != nil {
		return validate.Field("bridge_name", err.Error())
	}
	parsed := Range{}
	if gatewayOnly && enabled {
		return validate.Field("gateway_only", "gateway-only mode cannot be combined with managed DHCP")
	}
	if natEnabled && !enabled && !gatewayOnly {
		return validate.Field("nat_enabled", "NAT requires managed DHCP or gateway-only mode to be enabled")
	}
	httpBootURL = strings.TrimSpace(httpBootURL)
	if httpBootURL != "" {
		if !enabled {
			return validate.Field("http_boot_url", "HTTP boot requires managed DHCP to be enabled")
		}
		if err := validateHTTPBootURL(httpBootURL); err != nil {
			return validate.Field("http_boot_url", err.Error())
		}
	}
	if dnsConfig.Enabled && !enabled {
		return validate.Field("dns_enabled", "DNS requires managed DHCP to be enabled")
	}
	if dnsConfig.Auto && !dnsConfig.Enabled {
		return validate.Field("auto_dns", "Auto DNS requires managed DNS to be enabled")
	}
	if gatewayOnly {
		if strings.TrimSpace(cidr) == "" {
			return validate.Field("cidr", "gateway-only mode requires an IPv4 CIDR")
		}
		var err error
		parsed, err = ParseGatewayRange(cidr)
		if err != nil {
			return validate.Field("cidr", err.Error())
		}
		if err := m.provider.ValidateBridgeDHCP(bridge, parsed); err != nil {
			return err
		}
		dnsConfig = dns.Config{}
	}
	if enabled {
		if strings.TrimSpace(cidr) == "" {
			cidr = DefaultCIDR
		}
		var err error
		parsed, err = ParseRange(cidr)
		if err != nil {
			return validate.Field("cidr", err.Error())
		}
		if err := m.provider.ValidateBridgeDHCP(bridge, parsed); err != nil {
			return err
		}
		if dnsConfig.Enabled {
			forwarders, err := dns.NormalizeForwarders(dnsConfig.Forwarders, parsed.Server)
			if err != nil {
				return validate.Field("dns_forwarders", err.Error())
			}
			dnsConfig.Forwarders = forwarders
			if dnsConfig.Auto {
				if strings.TrimSpace(dnsConfig.Suffix) == "" {
					dnsConfig.Suffix = dns.DefaultSuffix(bridge)
				}
				dnsConfig.Suffix, err = dns.NormalizeSuffix(dnsConfig.Suffix)
				if err != nil {
					return validate.Field("dns_suffix", err.Error())
				}
			} else {
				dnsConfig.Suffix = ""
			}
		} else {
			dnsConfig = dns.Config{}
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if runtime := m.runtimes[bridge]; runtime != nil && len(runtime.users) != 0 {
		return fmt.Errorf("stop all running workloads on bridge %s before changing its DHCP configuration", bridge)
	}
	configured := enabled || gatewayOnly
	if configured {
		for otherBridge, otherConfig := range m.state.Bridges {
			if otherBridge == bridge || !otherConfig.Enabled {
				continue
			}
			otherRange, parseErr := otherConfig.network()
			if parseErr == nil && prefixesOverlap(parsed.Prefix, otherRange.Prefix) {
				return fmt.Errorf("range %s overlaps range %s configured for bridge %s", parsed.Prefix, otherRange.Prefix, otherBridge)
			}
		}
	}
	previous, found := m.state.Bridges[bridge]
	previousLeases := m.state.Leases[bridge]
	if configured {
		m.state.Bridges[bridge] = BridgeConfig{Enabled: true, GatewayOnly: gatewayOnly, CIDR: parsed.Prefix.String(), NAT: natEnabled, DNS: dnsConfig, HTTPBootURL: httpBootURL}
	} else {
		delete(m.state.Bridges, bridge)
	}
	if !found || previous.CIDR != parsed.Prefix.String() || previous.Enabled != configured || previous.GatewayOnly != gatewayOnly {
		delete(m.state.Leases, bridge)
	}
	delete(m.errors, bridge)
	if err := m.saveLocked(); err != nil {
		if found {
			m.state.Bridges[bridge] = previous
		} else {
			delete(m.state.Bridges, bridge)
		}
		if previousLeases != nil {
			m.state.Leases[bridge] = previousLeases
		}
		return err
	}
	return nil
}

// Enabled reports whether App Runner manages the bridge's subnet, either by
// serving DHCP or in gateway-only mode.
func (m *Manager) Enabled(bridge string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.Bridges[bridge].Enabled
}

// Prefix returns the subnet App Runner manages on a bridge, in either managed
// DHCP or gateway-only mode, so host-side services such as virtual BMCs can
// place addresses on it even before the gateway address is assigned.
func (m *Manager) Prefix(bridge string) (netip.Prefix, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	config, found := m.state.Bridges[bridge]
	if !found || !config.Enabled {
		return netip.Prefix{}, false
	}
	parsed, err := config.network()
	if err != nil {
		return netip.Prefix{}, false
	}
	return parsed.Prefix, true
}

func (m *Manager) Status(bridge string) BridgeStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	config, found := m.state.Bridges[bridge]
	status := BridgeStatus{
		CIDR: m.suggestCIDRLocked(bridge), LastError: m.errors[bridge],
		DNSForwarders: append([]string(nil), m.defaultForwarders...), DNSSuffix: dns.DefaultSuffix(bridge),
	}
	if !found || !config.Enabled {
		return status
	}
	status.Enabled = !config.GatewayOnly
	status.GatewayOnly = config.GatewayOnly
	status.NATEnabled = config.NAT
	status.DNSEnabled = config.DNS.Enabled
	status.AutoDNS = config.DNS.Auto
	if config.DNS.Enabled {
		status.DNSForwarders = append([]string(nil), config.DNS.Forwarders...)
	}
	if config.DNS.Auto {
		status.DNSSuffix = config.DNS.Suffix
	}
	status.HTTPBootURL = config.HTTPBootURL
	status.CIDR = config.CIDR
	parsed, err := config.network()
	if err != nil {
		status.LastError = err.Error()
		return status
	}
	status.ServerAddress = parsed.Server.String()
	if !config.GatewayOnly {
		status.PoolStart = parsed.PoolStart.String()
		status.PoolEnd = parsed.PoolEnd.String()
	}
	status.Running = m.runtimes[bridge] != nil && m.runtimes[bridge].running
	status.NATRunning = m.nat != nil && m.nat.Running(bridge)
	if m.dns != nil {
		status.DNSRunning, status.LastError = m.dns.Status(bridge)
		if status.LastError == "" {
			status.LastError = m.errors[bridge]
		}
	}
	now := m.now()
	for _, lease := range m.state.Leases[bridge] {
		if lease.Static || lease.ExpiresAt.After(now) {
			status.ActiveLeases++
			status.Leases = append(status.Leases, lease)
		}
	}
	sort.Slice(status.Leases, func(i, j int) bool {
		return status.Leases[i].HardwareAddress < status.Leases[j].HardwareAddress
	})
	return status
}

// Addresses lists the active managed-DHCP leases a MAC address holds on a
// bridge.
func (m *Manager) Addresses(bridge, macAddress string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	var result []string
	for _, lease := range m.state.Leases[bridge] {
		if strings.EqualFold(lease.HardwareAddress, macAddress) && (lease.Static || lease.ExpiresAt.After(now)) {
			result = append(result, lease.Address)
		}
	}
	sort.Strings(result)
	return result
}

func (m *Manager) suggestCIDRLocked(bridge string) string {
	used := make([]netip.Prefix, 0, len(m.state.Bridges))
	for otherBridge, config := range m.state.Bridges {
		if otherBridge == bridge || !config.Enabled {
			continue
		}
		if parsed, err := config.network(); err == nil {
			used = append(used, parsed.Prefix)
		}
	}
	for thirdOctet := 100; thirdOctet <= 254; thirdOctet++ {
		candidate := netip.MustParsePrefix(fmt.Sprintf("192.168.%d.0/24", thirdOctet))
		available := true
		for _, existing := range used {
			if prefixesOverlap(candidate, existing) {
				available = false
				break
			}
		}
		if available {
			return candidate.String()
		}
	}
	return "10.0.0.0/24"
}

// Prepare starts the managed services for every bridge the VM attaches to
// and records the VM as a user of each. A failure on a later bridge releases
// the earlier ones so nothing is left half-started.
func (m *Manager) Prepare(machine vm.VirtualMachine) error {
	prepared := make([]string, 0)
	for _, bridge := range machine.Bridges() {
		if err := m.prepareBridge(bridge, machine); err != nil {
			for _, previous := range prepared {
				m.releaseBridge(previous, machine.ID)
			}
			return err
		}
		prepared = append(prepared, bridge)
	}
	return nil
}

func (m *Manager) prepareBridge(bridge string, machine vm.VirtualMachine) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	config := m.state.Bridges[bridge]
	if !config.Enabled {
		return nil
	}
	parsed, err := config.network()
	if err != nil {
		return err
	}
	if config.DNS.Auto {
		if err := validate.DNSLabel(machine.Name); err != nil {
			return fmt.Errorf("VM name %q cannot be published by Auto DNS: %w", machine.Name, err)
		}
	}
	runtime := m.runtimes[bridge]
	// A gateway-only runtime has no DHCP server; existing at all means its
	// address and NAT are already in place.
	if runtime != nil && (runtime.running || config.GatewayOnly) {
		if config.DNS.Enabled {
			if m.dns == nil {
				return errors.New("DNS manager is not configured")
			}
			dnsRunning, _ := m.dns.Status(bridge)
			if !dnsRunning {
				if err := m.dns.Start(bridge, parsed.Server, config.DNS, func() map[string]netip.Addr {
					return m.autoDNSRecords(bridge)
				}); err != nil {
					m.errors[bridge] = err.Error()
					return fmt.Errorf("restart DNS for %s: %w", bridge, err)
				}
			}
		}
		runtime.users[machine.ID] = struct{}{}
		runtime.vms[machine.ID] = machine
		delete(m.errors, bridge)
		return nil
	}
	if runtime == nil {
		runtime = &bridgeRuntime{users: make(map[string]struct{}), vms: make(map[string]vm.VirtualMachine)}
	}
	if err := m.provider.EnsureBridgeDHCPAddress(bridge, parsed); err != nil {
		m.errors[bridge] = err.Error()
		return fmt.Errorf("prepare bridge DHCP address: %w", err)
	}
	natStarted := false
	if config.NAT {
		if m.nat == nil {
			err := errors.New("NAT manager is not configured")
			m.errors[bridge] = err.Error()
			return err
		}
		wasRunning := m.nat.Running(bridge)
		if err := m.nat.Start(bridge, parsed.Prefix); err != nil {
			m.errors[bridge] = err.Error()
			return fmt.Errorf("start NAT for %s: %w", bridge, err)
		}
		natStarted = !wasRunning
	}
	dnsStarted := false
	if config.DNS.Enabled {
		if m.dns == nil {
			err := errors.New("DNS manager is not configured")
			if natStarted {
				err = errors.Join(err, m.nat.Stop(bridge))
			}
			m.errors[bridge] = err.Error()
			return err
		}
		wasRunning, _ := m.dns.Status(bridge)
		if err := m.dns.Start(bridge, parsed.Server, config.DNS, func() map[string]netip.Addr {
			return m.autoDNSRecords(bridge)
		}); err != nil {
			if natStarted {
				err = errors.Join(err, m.nat.Stop(bridge))
			}
			m.errors[bridge] = err.Error()
			return fmt.Errorf("start DNS for %s: %w", bridge, err)
		}
		dnsStarted = !wasRunning
	}
	if config.GatewayOnly {
		runtime.users[machine.ID] = struct{}{}
		runtime.vms[machine.ID] = machine
		m.runtimes[bridge] = runtime
		runtime.nat = config.NAT
		delete(m.errors, bridge)
		slog.Info("bridge gateway prepared without managed DHCP", "bridge", bridge, "gateway", parsed.Server, "nat", config.NAT)
		return nil
	}
	handler := func(connection net.PacketConn, peer net.Addr, request *dhcpv4.DHCPv4) {
		m.handlePacket(bridge, parsed, config, connection, peer, request)
	}
	server, err := m.newServer(bridge, handler)
	if err != nil {
		if dnsStarted {
			err = errors.Join(err, m.dns.Stop(bridge))
		}
		if natStarted {
			err = errors.Join(err, m.nat.Stop(bridge))
		}
		m.errors[bridge] = err.Error()
		return fmt.Errorf("start DHCP listener on %s: %w", bridge, err)
	}
	runtime.users[machine.ID] = struct{}{}
	runtime.vms[machine.ID] = machine
	m.runtimes[bridge] = runtime
	runtime.server = server
	runtime.running = true
	runtime.nat = config.NAT
	runtime.dns = config.DNS.Enabled
	delete(m.errors, bridge)
	go m.serve(bridge, runtime)
	slog.Info("bridge DHCP server started", "bridge", bridge, "cidr", parsed.Prefix.String(), "pool_start", parsed.PoolStart, "pool_end", parsed.PoolEnd)
	return nil
}

// Release drops the VM from every bridge it used, stopping services on
// bridges that no longer have any running VM.
func (m *Manager) Release(machine vm.VirtualMachine) {
	for _, bridge := range machine.Bridges() {
		m.releaseBridge(bridge, machine.ID)
	}
}

func (m *Manager) releaseBridge(bridge, vmID string) {
	m.mu.Lock()
	runtime := m.runtimes[bridge]
	if runtime == nil {
		m.mu.Unlock()
		return
	}
	delete(runtime.users, vmID)
	delete(runtime.vms, vmID)
	if len(runtime.users) != 0 {
		m.mu.Unlock()
		return
	}
	delete(m.runtimes, bridge)
	m.mu.Unlock()
	if runtime.server != nil {
		if err := runtime.server.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			slog.Warn("close bridge DHCP server", "bridge", bridge, "error", err)
		}
	}
	if runtime.dns && m.dns != nil {
		if err := m.dns.Stop(bridge); err != nil {
			m.mu.Lock()
			m.errors[bridge] = err.Error()
			m.mu.Unlock()
			slog.Warn("stop bridge DNS", "bridge", bridge, "error", err)
		}
	}
	if runtime.nat && m.nat != nil {
		if err := m.nat.Stop(bridge); err != nil {
			m.mu.Lock()
			m.errors[bridge] = err.Error()
			m.mu.Unlock()
			slog.Warn("stop bridge NAT", "bridge", bridge, "error", err)
		}
	}
	slog.Info("bridge DHCP server stopped", "bridge", bridge)
}

func (m *Manager) Reconcile(vms []vm.VirtualMachine) error {
	var result error
	for _, machine := range vms {
		if machine.Status != vm.StatusRunning && machine.Status != vm.StatusStopping {
			continue
		}
		if err := m.Prepare(machine); err != nil {
			result = errors.Join(result, fmt.Errorf("restore DHCP for VM %s: %w", machine.Name, err))
		}
	}
	if m.nat != nil {
		result = errors.Join(result, m.nat.FinishRecovery())
	}
	return result
}

// Shutdown closes the DHCP and DNS listeners when the backend exits. NAT
// rules and IPv4 forwarding are intentionally left in place: guests keep
// running across a backend restart and still need them, and the next start
// re-applies or removes them once it knows which VMs are still up.
func (m *Manager) Shutdown() error {
	m.mu.Lock()
	runtimes := m.runtimes
	m.runtimes = make(map[string]*bridgeRuntime)
	m.mu.Unlock()
	var result error
	for _, runtime := range runtimes {
		if runtime.server == nil {
			continue
		}
		if err := runtime.server.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			result = errors.Join(result, err)
		}
	}
	if m.dns != nil {
		result = errors.Join(result, m.dns.Close())
	}
	return result
}

func (m *Manager) serve(bridge string, runtime *bridgeRuntime) {
	err := runtime.server.Serve()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runtimes[bridge] != runtime {
		return
	}
	runtime.server = nil
	runtime.running = false
	if err != nil && !errors.Is(err, net.ErrClosed) {
		m.errors[bridge] = err.Error()
		slog.Error("bridge DHCP server stopped unexpectedly", "bridge", bridge, "error", err)
	}
}

func (m *Manager) autoDNSRecords(bridge string) map[string]netip.Addr {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]netip.Addr)
	runtime := m.runtimes[bridge]
	if runtime == nil {
		return result
	}
	now := m.now()
	addressesByMAC := make(map[string]netip.Addr)
	for _, lease := range m.state.Leases[bridge] {
		if !lease.Static && !lease.ExpiresAt.After(now) {
			continue
		}
		address, err := netip.ParseAddr(lease.Address)
		if err == nil {
			addressesByMAC[strings.ToLower(lease.HardwareAddress)] = address
		}
	}
	for _, machine := range runtime.vms {
		for _, nic := range machine.NICs {
			if nic.Mode != vm.NetworkModeBridge || nic.BridgeName != bridge {
				continue
			}
			if address, found := addressesByMAC[strings.ToLower(nic.MACAddress)]; found {
				result[strings.ToLower(machine.Name)] = address
				break
			}
		}
	}
	return result
}

func (m *Manager) handlePacket(bridge string, network Range, config BridgeConfig, connection net.PacketConn, peer net.Addr, request *dhcpv4.DHCPv4) {
	if request == nil || request.OpCode != dhcpv4.OpcodeBootRequest {
		return
	}
	messageType := request.MessageType()
	requested := requestedAddress(request)
	requestedAddressValue := ""
	if requested.IsValid() {
		requestedAddressValue = requested.String()
	}
	slog.Info("DHCP request received",
		"bridge", bridge,
		"client", request.ClientHWAddr.String(),
		"peer", peer,
		"transaction_id", request.TransactionID.String(),
		"message_type", messageType.String(),
		"requested_address", requestedAddressValue,
		"hostname", request.HostName(),
		"vendor_class", request.ClassIdentifier(),
	)
	if messageType == dhcpv4.MessageTypeRelease || messageType == dhcpv4.MessageTypeDecline {
		m.releaseLease(bridge, clientKey(request))
		return
	}
	if messageType != dhcpv4.MessageTypeDiscover && messageType != dhcpv4.MessageTypeRequest && messageType != dhcpv4.MessageTypeInform {
		return
	}
	if messageType == dhcpv4.MessageTypeRequest {
		if selected := request.ServerIdentifier(); selected != nil && !selected.Equal(net.IP(network.Server.AsSlice())) {
			return
		}
	}

	replyType := dhcpv4.MessageTypeOffer
	leaseAddress := netip.Addr{}
	if messageType == dhcpv4.MessageTypeInform {
		replyType = dhcpv4.MessageTypeAck
	} else {
		strictRequested := false
		if messageType == dhcpv4.MessageTypeRequest {
			strictRequested = requested.IsValid()
		} else {
			requested = netip.Addr{}
		}
		var err error
		leaseAddress, err = m.acquireLease(bridge, network, clientKey(request), request.ClientHWAddr.String(), requested, strictRequested)
		if err != nil {
			if messageType != dhcpv4.MessageTypeRequest {
				slog.Warn("DHCP address allocation failed", "bridge", bridge, "client", request.ClientHWAddr, "error", err)
				return
			}
			replyType = dhcpv4.MessageTypeNak
		} else if messageType == dhcpv4.MessageTypeRequest {
			replyType = dhcpv4.MessageTypeAck
		}
	}

	reply, err := dhcpv4.NewReplyFromRequest(request)
	if err != nil {
		slog.Warn("build DHCP response", "bridge", bridge, "error", err)
		return
	}
	reply.UpdateOption(dhcpv4.OptMessageType(replyType))
	reply.UpdateOption(dhcpv4.OptServerIdentifier(net.IP(network.Server.AsSlice())))
	if replyType != dhcpv4.MessageTypeNak && messageType != dhcpv4.MessageTypeInform {
		reply.YourIPAddr = net.IP(leaseAddress.AsSlice())
	}
	if replyType != dhcpv4.MessageTypeNak {
		prefixBits := network.Prefix.Bits()
		reply.UpdateOption(dhcpv4.OptSubnetMask(net.CIDRMask(prefixBits, 32)))
		reply.UpdateOption(dhcpv4.OptBroadcastAddress(net.IP(network.Broadcast.AsSlice())))
		if config.NAT {
			reply.UpdateOption(dhcpv4.OptRouter(net.IP(network.Server.AsSlice())))
		}
		if config.DNS.Enabled {
			reply.UpdateOption(dhcpv4.OptDNS(net.IP(network.Server.AsSlice())))
			if config.DNS.Auto {
				reply.UpdateOption(dhcpv4.OptDomainName(config.DNS.Suffix))
			}
		}
		if messageType != dhcpv4.MessageTypeInform {
			reply.UpdateOption(dhcpv4.OptIPAddressLeaseTime(leaseDuration))
		}
		// UEFI HTTP boot (UEFI spec 2.5+ §24.7) identifies itself with the
		// HTTPClient vendor class and expects the same class echoed back with
		// the boot file URI in option 67.
		if config.HTTPBootURL != "" && strings.HasPrefix(request.ClassIdentifier(), httpBootClass) {
			reply.UpdateOption(dhcpv4.OptClassIdentifier(httpBootClass))
			reply.UpdateOption(dhcpv4.OptBootFileName(config.HTTPBootURL))
		}
	}
	packet := reply.ToBytes()
	if _, err := connection.WriteTo(packet, peer); err != nil {
		slog.Warn("send DHCP response", "bridge", bridge, "client", request.ClientHWAddr, "error", err)
		return
	}
	assignedAddress := ""
	if leaseAddress.IsValid() {
		assignedAddress = leaseAddress.String()
	}
	slog.Info("DHCP response sent",
		"bridge", bridge,
		"client", request.ClientHWAddr.String(),
		"peer", peer,
		"transaction_id", request.TransactionID.String(),
		"message_type", replyType.String(),
		"assigned_address", assignedAddress,
		"bytes", len(packet),
	)
}

func (m *Manager) acquireLease(bridge string, network Range, key, hardwareAddress string, requested netip.Addr, strictRequested bool) (netip.Addr, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	hardwareAddress = strings.ToLower(hardwareAddress)
	leases := m.state.Leases[bridge]
	if leases == nil {
		leases = make(map[string]Lease)
		m.state.Leases[bridge] = leases
	}
	for client, lease := range leases {
		address, err := netip.ParseAddr(lease.Address)
		if err != nil || (!lease.Static && (!lease.ExpiresAt.After(now) || !addressInPool(network, address))) ||
			(lease.Static && !addressInStaticRange(network, address)) {
			delete(leases, client)
		}
	}
	if existing, found := leases[key]; found && strings.EqualFold(existing.HardwareAddress, hardwareAddress) {
		return m.renewHardwareLeaseLocked(leases, key, hardwareAddress, requested, strictRequested, now)
	}
	if _, found := leases[key]; !found {
		for _, lease := range leases {
			if strings.EqualFold(lease.HardwareAddress, hardwareAddress) {
				return m.renewHardwareLeaseLocked(leases, key, hardwareAddress, requested, strictRequested, now)
			}
		}
	}
	if existing, found := leases[key]; found {
		address, _ := netip.ParseAddr(existing.Address)
		if strictRequested && requested.IsValid() && requested != address {
			return netip.Addr{}, fmt.Errorf("requested address %s does not match the existing lease %s", requested, address)
		}
		existing.ExpiresAt = now.Add(leaseDuration)
		existing.HardwareAddress = hardwareAddress
		leases[key] = existing
		if err := m.saveLocked(); err != nil {
			return netip.Addr{}, err
		}
		return address, nil
	}
	used := make(map[netip.Addr]struct{}, len(leases))
	for _, lease := range leases {
		if address, err := netip.ParseAddr(lease.Address); err == nil {
			used[address] = struct{}{}
		}
	}
	address := netip.Addr{}
	if requested.IsValid() && addressInPool(network, requested) {
		if _, exists := used[requested]; !exists {
			address = requested
		} else if strictRequested {
			return netip.Addr{}, fmt.Errorf("requested address %s is already leased", requested)
		}
	} else if strictRequested {
		return netip.Addr{}, fmt.Errorf("requested address %s is outside the DHCP pool", requested)
	}
	if !address.IsValid() {
		for candidate := network.PoolStart; compareIPv4(candidate, network.PoolEnd) <= 0; candidate = candidate.Next() {
			if _, exists := used[candidate]; !exists {
				address = candidate
				break
			}
		}
	}
	if !address.IsValid() {
		return netip.Addr{}, errors.New("DHCP address pool is exhausted")
	}
	leases[key] = Lease{ClientKey: key, HardwareAddress: hardwareAddress, Address: address.String(), ExpiresAt: now.Add(leaseDuration)}
	if err := m.saveLocked(); err != nil {
		delete(leases, key)
		return netip.Addr{}, err
	}
	return address, nil
}

func (m *Manager) renewHardwareLeaseLocked(leases map[string]Lease, key, hardwareAddress string, requested netip.Addr, strictRequested bool, now time.Time) (netip.Addr, error) {
	selectedKey := ""
	selected := Lease{}
	for client, lease := range leases {
		if !strings.EqualFold(lease.HardwareAddress, hardwareAddress) {
			continue
		}
		address, err := netip.ParseAddr(lease.Address)
		if err != nil {
			continue
		}
		if requested.IsValid() && address == requested {
			selectedKey, selected = client, lease
			break
		}
		if selectedKey == "" || (client == key && selectedKey != key) || (selectedKey != key && lease.ExpiresAt.After(selected.ExpiresAt)) {
			selectedKey, selected = client, lease
		}
	}
	address, err := netip.ParseAddr(selected.Address)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("parse existing DHCP lease address: %w", err)
	}
	if strictRequested && requested.IsValid() && requested != address {
		return netip.Addr{}, fmt.Errorf("requested address %s does not match the existing lease %s", requested, address)
	}
	for client, lease := range leases {
		if client != selectedKey && strings.EqualFold(lease.HardwareAddress, hardwareAddress) {
			delete(leases, client)
		}
	}
	if selectedKey != key {
		delete(leases, selectedKey)
	}
	selected.ClientKey = key
	selected.HardwareAddress = hardwareAddress
	if !selected.Static {
		selected.ExpiresAt = now.Add(leaseDuration)
	}
	leases[key] = selected
	if err := m.saveLocked(); err != nil {
		return netip.Addr{}, err
	}
	return address, nil
}

func (m *Manager) releaseLease(bridge, key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lease, found := m.state.Leases[bridge][key]
	if !found || lease.Static {
		return
	}
	delete(m.state.Leases[bridge], key)
	if err := m.saveLocked(); err != nil {
		slog.Warn("persist released DHCP lease", "bridge", bridge, "client", key, "error", err)
	}
}

func (m *Manager) ClearDynamicLeases(bridge string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for client, lease := range m.state.Leases[bridge] {
		if !lease.Static {
			delete(m.state.Leases[bridge], client)
		}
	}
	return m.saveLocked()
}

func (m *Manager) DeleteLease(bridge, hardwareAddress string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	hardwareAddress = strings.ToLower(strings.TrimSpace(hardwareAddress))
	deleted := false
	for client, lease := range m.state.Leases[bridge] {
		if strings.EqualFold(lease.HardwareAddress, hardwareAddress) {
			delete(m.state.Leases[bridge], client)
			deleted = true
		}
	}
	if !deleted {
		return fmt.Errorf("%w: no DHCP lease for hardware address %s", ErrLeaseNotFound, hardwareAddress)
	}
	return m.saveLocked()
}

func (m *Manager) SetStaticLease(bridge, hardwareAddress, addressValue string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	config, found := m.state.Bridges[bridge]
	if !found || !config.Enabled || config.GatewayOnly {
		return fmt.Errorf("DHCP is not enabled on bridge %s", bridge)
	}
	network, err := ParseRange(config.CIDR)
	if err != nil {
		return err
	}
	hardwareAddress = strings.ToLower(strings.TrimSpace(hardwareAddress))
	if _, err := net.ParseMAC(hardwareAddress); err != nil {
		return validate.Field("hardware_address", "must be a valid MAC address")
	}
	address, err := netip.ParseAddr(strings.TrimSpace(addressValue))
	if err != nil || !addressInStaticRange(network, address) {
		return validate.Field("address", fmt.Sprintf("must be within %s before the DHCP pool (%s)", network.Prefix, network.PoolStart))
	}
	leases := m.state.Leases[bridge]
	if leases == nil {
		leases = make(map[string]Lease)
		m.state.Leases[bridge] = leases
	}
	for client, lease := range leases {
		if strings.EqualFold(lease.HardwareAddress, hardwareAddress) {
			delete(leases, client)
			continue
		}
		if lease.Address == address.String() {
			return fmt.Errorf("address %s is already assigned", address)
		}
	}
	leases["mac:"+hardwareAddress] = Lease{ClientKey: "mac:" + hardwareAddress, HardwareAddress: hardwareAddress, Address: address.String(), Static: true}
	return m.saveLocked()
}

func (m *Manager) load() error {
	contents, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read DHCP state: %w", err)
	}
	if err := json.Unmarshal(contents, &m.state); err != nil {
		return fmt.Errorf("decode DHCP state: %w", err)
	}
	if m.state.Bridges == nil {
		m.state.Bridges = make(map[string]BridgeConfig)
	}
	if m.state.Leases == nil {
		m.state.Leases = make(map[string]map[string]Lease)
	}
	changed := false
	for bridge, leases := range m.state.Leases {
		for client, lease := range leases {
			if lease.HardwareAddress == "" {
				continue
			}
			lease.HardwareAddress = strings.ToLower(lease.HardwareAddress)
			if canonicalClient, found := canonicalLeaseClient(leases, lease.HardwareAddress); found && canonicalClient != client {
				delete(leases, client)
				changed = true
				continue
			}
			leases[client] = lease
		}
		m.state.Leases[bridge] = leases
	}
	if changed {
		if err := m.saveLocked(); err != nil {
			return fmt.Errorf("persist consolidated DHCP leases: %w", err)
		}
	}
	return nil
}

func canonicalLeaseClient(leases map[string]Lease, hardwareAddress string) (string, bool) {
	var selectedKey string
	var selected Lease
	for client, lease := range leases {
		if !strings.EqualFold(lease.HardwareAddress, hardwareAddress) {
			continue
		}
		if selectedKey == "" || lease.ExpiresAt.After(selected.ExpiresAt) || (lease.ExpiresAt.Equal(selected.ExpiresAt) && client < selectedKey) {
			selectedKey, selected = client, lease
		}
	}
	return selectedKey, selectedKey != ""
}

func (m *Manager) saveLocked() error {
	contents, err := json.MarshalIndent(m.state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode DHCP state: %w", err)
	}
	contents = append(contents, '\n')
	if err := fsutil.WriteFileAtomic(m.path, contents, 0o600); err != nil {
		return fmt.Errorf("write DHCP state: %w", err)
	}
	return nil
}

func validateHTTPBootURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return err
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Path == "" || parsed.Path == "/" {
		return errors.New("must be an http or https URL naming a boot file")
	}
	if len(value) > 255 {
		return errors.New("must be at most 255 characters")
	}
	return nil
}

func ParseRange(value string) (Range, error) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
	if err != nil || !prefix.Addr().Is4() {
		return Range{}, errors.New("DHCP range must be an IPv4 CIDR such as 192.168.100.0/24")
	}
	prefix = prefix.Masked()
	if prefix.Bits() < 8 || prefix.Bits() > 26 {
		return Range{}, errors.New("DHCP range prefix must be between /8 and /26 so the lease pool can begin at host offset 50")
	}
	network := ipv4ToUint32(prefix.Addr())
	hostBits := uint32(32 - prefix.Bits())
	broadcast := network | (uint32(1)<<hostBits - 1)
	result := Range{
		Prefix: prefix, Server: uint32ToIPv4(network + 1), PoolStart: uint32ToIPv4(network + poolStartOffset),
		PoolEnd: uint32ToIPv4(broadcast - 1), Broadcast: uint32ToIPv4(broadcast),
	}
	if compareIPv4(result.PoolStart, result.PoolEnd) > 0 {
		return Range{}, errors.New("DHCP range has no usable addresses at or after host offset 50")
	}
	return result, nil
}

// ParseGatewayRange parses the subnet of a gateway-only bridge. The bridge
// takes the first host address as its gateway; there is no lease pool
// because another DHCP server allocates the rest of the subnet.
func ParseGatewayRange(value string) (Range, error) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
	if err != nil || !prefix.Addr().Is4() {
		return Range{}, errors.New("range must be an IPv4 CIDR such as 172.22.0.0/24")
	}
	prefix = prefix.Masked()
	if prefix.Bits() < 8 || prefix.Bits() > 30 {
		return Range{}, errors.New("gateway range prefix must be between /8 and /30")
	}
	network := ipv4ToUint32(prefix.Addr())
	broadcast := network | (uint32(1)<<uint32(32-prefix.Bits()) - 1)
	return Range{Prefix: prefix, Server: uint32ToIPv4(network + 1), Broadcast: uint32ToIPv4(broadcast)}, nil
}

func requestedAddress(request *dhcpv4.DHCPv4) netip.Addr {
	requested := request.RequestedIPAddress()
	if requested == nil || requested.Equal(net.IPv4zero) {
		requested = request.ClientIPAddr
	}
	if address, found := netip.AddrFromSlice(requested.To4()); found {
		return address
	}
	return netip.Addr{}
}

func clientKey(request *dhcpv4.DHCPv4) string {
	if identifier := request.Options.Get(dhcpv4.OptionClientIdentifier); len(identifier) != 0 {
		return "id:" + hex.EncodeToString(identifier)
	}
	return "mac:" + strings.ToLower(request.ClientHWAddr.String())
}

func addressInPool(network Range, address netip.Addr) bool {
	return address.Is4() && network.PoolStart.IsValid() && compareIPv4(address, network.PoolStart) >= 0 && compareIPv4(address, network.PoolEnd) <= 0
}

func addressInStaticRange(network Range, address netip.Addr) bool {
	return address.Is4() && network.Prefix.Contains(address) && address != network.Server && compareIPv4(address, network.PoolStart) < 0
}

func prefixesOverlap(left, right netip.Prefix) bool {
	return left.Contains(right.Addr()) || right.Contains(left.Addr())
}

func compareIPv4(left, right netip.Addr) int {
	l := ipv4ToUint32(left)
	r := ipv4ToUint32(right)
	if l < r {
		return -1
	}
	if l > r {
		return 1
	}
	return 0
}

func ipv4ToUint32(address netip.Addr) uint32 {
	bytes := address.As4()
	return binary.BigEndian.Uint32(bytes[:])
}

func uint32ToIPv4(value uint32) netip.Addr {
	var bytes [4]byte
	binary.BigEndian.PutUint32(bytes[:], value)
	return netip.AddrFrom4(bytes)
}

// AddressConflicts reports whether an existing IPv4 address (with its
// prefix length) prevents serving the DHCP range. Addresses on other
// interfaces or with a different prefix length conflict whenever the subnets
// overlap. On the bridge itself the server address is expected, and other
// host-side service addresses in the subnet, such as virtual BMC endpoints,
// are fine as long as they sit outside the lease pool; a gateway-only range
// has no pool, so any address in its subnet is fine there.
func AddressConflicts(network Range, onBridge bool, existing netip.Prefix) bool {
	subnet := existing.Masked()
	if !network.Prefix.Contains(subnet.Addr()) && !subnet.Contains(network.Prefix.Addr()) {
		return false
	}
	if !onBridge || existing.Bits() != network.Prefix.Bits() {
		return true
	}
	if existing.Addr() == network.Server {
		return false
	}
	return addressInPool(network, existing.Addr()) || !network.Prefix.Contains(existing.Addr())
}
