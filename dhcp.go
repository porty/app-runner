package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/server4"
)

const (
	defaultBridgeDHCPCIDR = "192.168.100.0/24"
	dhcpPoolStartOffset   = uint32(50)
	dhcpLeaseDuration     = 24 * time.Hour
)

type bridgeDHCPConfig struct {
	Enabled bool   `json:"enabled"`
	CIDR    string `json:"cidr"`
	NAT     bool   `json:"nat,omitempty"`
}

type dhcpLease struct {
	ClientKey       string    `json:"client_key"`
	HardwareAddress string    `json:"hardware_address,omitempty"`
	Address         string    `json:"address"`
	ExpiresAt       time.Time `json:"expires_at"`
}

type dhcpPersistedState struct {
	Bridges map[string]bridgeDHCPConfig     `json:"bridges"`
	Leases  map[string]map[string]dhcpLease `json:"leases,omitempty"`
}

type bridgeDHCPStatus struct {
	Enabled       bool
	CIDR          string
	ServerAddress string
	PoolStart     string
	PoolEnd       string
	Running       bool
	ActiveLeases  uint32
	LastError     string
	NATEnabled    bool
	NATRunning    bool
}

type dhcpRange struct {
	Prefix    netip.Prefix
	Server    netip.Addr
	PoolStart netip.Addr
	PoolEnd   netip.Addr
	Broadcast netip.Addr
}

type dhcpBridgeProvider interface {
	ValidateBridgeDHCP(string, dhcpRange) error
	EnsureBridgeDHCPAddress(string, dhcpRange) error
}

type managedDHCPServer interface {
	Serve() error
	Close() error
}

type dhcpServerFactory func(string, server4.Handler) (managedDHCPServer, error)

type dhcpRuntime struct {
	server  managedDHCPServer
	users   map[string]struct{}
	running bool
	nat     bool
}

type dhcpManager struct {
	mu        sync.Mutex
	path      string
	provider  dhcpBridgeProvider
	newServer dhcpServerFactory
	nat       bridgeNATController
	now       func() time.Time
	state     dhcpPersistedState
	runtimes  map[string]*dhcpRuntime
	errors    map[string]string
}

func newDHCPManager(diskDirectory string, provider dhcpBridgeProvider, nat bridgeNATController) (*dhcpManager, error) {
	manager := &dhcpManager{
		path: filepath.Join(diskDirectory, "dhcp.json"), provider: provider,
		newServer: newInProcessDHCPServer, now: time.Now,
		state:    dhcpPersistedState{Bridges: make(map[string]bridgeDHCPConfig), Leases: make(map[string]map[string]dhcpLease)},
		runtimes: make(map[string]*dhcpRuntime), errors: make(map[string]string),
	}
	manager.nat = nat
	if err := manager.load(); err != nil {
		return nil, err
	}
	return manager, nil
}

func newInProcessDHCPServer(bridge string, handler server4.Handler) (managedDHCPServer, error) {
	return server4.NewServer(bridge, &net.UDPAddr{IP: net.IPv4zero, Port: dhcpv4.ServerPort}, handler)
}

func (m *dhcpManager) Configure(bridge string, enabled bool, cidr string, natEnabled bool) error {
	if err := validateInterfaceName(bridge); err != nil {
		return fmt.Errorf("bridge name %w", err)
	}
	parsed := dhcpRange{}
	if natEnabled && !enabled {
		return errors.New("NAT requires managed DHCP to be enabled")
	}
	if enabled {
		if strings.TrimSpace(cidr) == "" {
			cidr = defaultBridgeDHCPCIDR
		}
		var err error
		parsed, err = parseDHCPRange(cidr)
		if err != nil {
			return err
		}
		if err := m.provider.ValidateBridgeDHCP(bridge, parsed); err != nil {
			return err
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if runtime := m.runtimes[bridge]; runtime != nil && len(runtime.users) != 0 {
		return fmt.Errorf("stop all running workloads on bridge %s before changing its DHCP configuration", bridge)
	}
	if enabled {
		for otherBridge, otherConfig := range m.state.Bridges {
			if otherBridge == bridge || !otherConfig.Enabled {
				continue
			}
			otherRange, parseErr := parseDHCPRange(otherConfig.CIDR)
			if parseErr == nil && prefixesOverlap(parsed.Prefix, otherRange.Prefix) {
				return fmt.Errorf("DHCP range %s overlaps range %s configured for bridge %s", parsed.Prefix, otherRange.Prefix, otherBridge)
			}
		}
	}
	previous, found := m.state.Bridges[bridge]
	previousLeases := m.state.Leases[bridge]
	if enabled {
		m.state.Bridges[bridge] = bridgeDHCPConfig{Enabled: true, CIDR: parsed.Prefix.String(), NAT: natEnabled}
	} else {
		delete(m.state.Bridges, bridge)
	}
	if !found || previous.CIDR != parsed.Prefix.String() || previous.Enabled != enabled {
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

func (m *dhcpManager) Enabled(bridge string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.Bridges[bridge].Enabled
}

func (m *dhcpManager) Status(bridge string) bridgeDHCPStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	config, found := m.state.Bridges[bridge]
	status := bridgeDHCPStatus{CIDR: m.suggestCIDRLocked(bridge), LastError: m.errors[bridge]}
	if !found || !config.Enabled {
		return status
	}
	status.Enabled = true
	status.NATEnabled = config.NAT
	status.CIDR = config.CIDR
	parsed, err := parseDHCPRange(config.CIDR)
	if err != nil {
		status.LastError = err.Error()
		return status
	}
	status.ServerAddress = parsed.Server.String()
	status.PoolStart = parsed.PoolStart.String()
	status.PoolEnd = parsed.PoolEnd.String()
	status.Running = m.runtimes[bridge] != nil && m.runtimes[bridge].running
	status.NATRunning = m.nat != nil && m.nat.Running(bridge)
	now := m.now()
	for _, lease := range m.state.Leases[bridge] {
		if lease.ExpiresAt.After(now) {
			status.ActiveLeases++
		}
	}
	return status
}

func (m *dhcpManager) suggestCIDRLocked(bridge string) string {
	used := make([]netip.Prefix, 0, len(m.state.Bridges))
	for otherBridge, config := range m.state.Bridges {
		if otherBridge == bridge || !config.Enabled {
			continue
		}
		if parsed, err := parseDHCPRange(config.CIDR); err == nil {
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

func (m *dhcpManager) Prepare(vm virtualMachine) error {
	if vm.NetworkMode != networkModeBridge || vm.BridgeName == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	config := m.state.Bridges[vm.BridgeName]
	if !config.Enabled {
		return nil
	}
	runtime := m.runtimes[vm.BridgeName]
	if runtime != nil {
		runtime.users[vm.ID] = struct{}{}
		if runtime.running {
			return nil
		}
	}
	parsed, err := parseDHCPRange(config.CIDR)
	if err != nil {
		return err
	}
	if err := m.provider.EnsureBridgeDHCPAddress(vm.BridgeName, parsed); err != nil {
		m.errors[vm.BridgeName] = err.Error()
		return fmt.Errorf("prepare bridge DHCP address: %w", err)
	}
	natStarted := false
	if config.NAT {
		if m.nat == nil {
			err := errors.New("NAT manager is not configured")
			m.errors[vm.BridgeName] = err.Error()
			return err
		}
		if err := m.nat.Start(vm.BridgeName, parsed.Prefix); err != nil {
			m.errors[vm.BridgeName] = err.Error()
			return fmt.Errorf("start NAT for %s: %w", vm.BridgeName, err)
		}
		natStarted = true
	}
	handler := func(connection net.PacketConn, peer net.Addr, request *dhcpv4.DHCPv4) {
		m.handlePacket(vm.BridgeName, parsed, config.NAT, connection, peer, request)
	}
	server, err := m.newServer(vm.BridgeName, handler)
	if err != nil {
		if natStarted {
			err = errors.Join(err, m.nat.Stop(vm.BridgeName))
		}
		m.errors[vm.BridgeName] = err.Error()
		return fmt.Errorf("start DHCP listener on %s: %w", vm.BridgeName, err)
	}
	if runtime == nil {
		runtime = &dhcpRuntime{users: map[string]struct{}{vm.ID: {}}}
		m.runtimes[vm.BridgeName] = runtime
	}
	runtime.server = server
	runtime.running = true
	runtime.nat = config.NAT
	delete(m.errors, vm.BridgeName)
	go m.serve(vm.BridgeName, runtime)
	slog.Info("bridge DHCP server started", "bridge", vm.BridgeName, "cidr", parsed.Prefix.String(), "pool_start", parsed.PoolStart, "pool_end", parsed.PoolEnd)
	return nil
}

func (m *dhcpManager) Release(vm virtualMachine) {
	if vm.NetworkMode != networkModeBridge || vm.BridgeName == "" {
		return
	}
	m.mu.Lock()
	runtime := m.runtimes[vm.BridgeName]
	if runtime == nil {
		m.mu.Unlock()
		return
	}
	delete(runtime.users, vm.ID)
	if len(runtime.users) != 0 {
		m.mu.Unlock()
		return
	}
	delete(m.runtimes, vm.BridgeName)
	m.mu.Unlock()
	if runtime.server != nil {
		if err := runtime.server.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			slog.Warn("close bridge DHCP server", "bridge", vm.BridgeName, "error", err)
		}
	}
	if runtime.nat && m.nat != nil {
		if err := m.nat.Stop(vm.BridgeName); err != nil {
			m.mu.Lock()
			m.errors[vm.BridgeName] = err.Error()
			m.mu.Unlock()
			slog.Warn("stop bridge NAT", "bridge", vm.BridgeName, "error", err)
		}
	}
	slog.Info("bridge DHCP server stopped", "bridge", vm.BridgeName)
}

func (m *dhcpManager) Reconcile(vms []virtualMachine) error {
	var result error
	for _, vm := range vms {
		if vm.Status != vmStatusRunning && vm.Status != vmStatusStopping {
			continue
		}
		if err := m.Prepare(vm); err != nil {
			result = errors.Join(result, fmt.Errorf("restore DHCP for VM %s: %w", vm.Name, err))
		}
	}
	if m.nat != nil {
		result = errors.Join(result, m.nat.FinishRecovery())
	}
	return result
}

func (m *dhcpManager) Close() error {
	m.mu.Lock()
	runtimes := m.runtimes
	m.runtimes = make(map[string]*dhcpRuntime)
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
	if m.nat != nil {
		result = errors.Join(result, m.nat.Close())
	}
	return result
}

func (m *dhcpManager) serve(bridge string, runtime *dhcpRuntime) {
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

func (m *dhcpManager) handlePacket(bridge string, network dhcpRange, natEnabled bool, connection net.PacketConn, peer net.Addr, request *dhcpv4.DHCPv4) {
	if request == nil || request.OpCode != dhcpv4.OpcodeBootRequest {
		return
	}
	messageType := request.MessageType()
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
		requested := netip.Addr{}
		strictRequested := false
		if messageType == dhcpv4.MessageTypeRequest {
			requested = requestedAddress(request)
			strictRequested = requested.IsValid()
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
		if natEnabled {
			reply.UpdateOption(dhcpv4.OptRouter(net.IP(network.Server.AsSlice())))
		}
		if messageType != dhcpv4.MessageTypeInform {
			reply.UpdateOption(dhcpv4.OptIPAddressLeaseTime(dhcpLeaseDuration))
		}
	}
	if _, err := connection.WriteTo(reply.ToBytes(), peer); err != nil {
		slog.Warn("send DHCP response", "bridge", bridge, "client", request.ClientHWAddr, "error", err)
	}
}

func (m *dhcpManager) acquireLease(bridge string, network dhcpRange, key, hardwareAddress string, requested netip.Addr, strictRequested bool) (netip.Addr, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	leases := m.state.Leases[bridge]
	if leases == nil {
		leases = make(map[string]dhcpLease)
		m.state.Leases[bridge] = leases
	}
	for client, lease := range leases {
		address, err := netip.ParseAddr(lease.Address)
		if err != nil || !lease.ExpiresAt.After(now) || !addressInDHCPPool(network, address) {
			delete(leases, client)
		}
	}
	if existing, found := leases[key]; found {
		address, _ := netip.ParseAddr(existing.Address)
		if strictRequested && requested.IsValid() && requested != address {
			return netip.Addr{}, fmt.Errorf("requested address %s does not match the existing lease %s", requested, address)
		}
		existing.ExpiresAt = now.Add(dhcpLeaseDuration)
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
	if requested.IsValid() && addressInDHCPPool(network, requested) {
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
	leases[key] = dhcpLease{ClientKey: key, HardwareAddress: hardwareAddress, Address: address.String(), ExpiresAt: now.Add(dhcpLeaseDuration)}
	if err := m.saveLocked(); err != nil {
		delete(leases, key)
		return netip.Addr{}, err
	}
	return address, nil
}

func (m *dhcpManager) releaseLease(bridge, key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, found := m.state.Leases[bridge][key]; !found {
		return
	}
	delete(m.state.Leases[bridge], key)
	if err := m.saveLocked(); err != nil {
		slog.Warn("persist released DHCP lease", "bridge", bridge, "client", key, "error", err)
	}
}

func (m *dhcpManager) load() error {
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
		m.state.Bridges = make(map[string]bridgeDHCPConfig)
	}
	if m.state.Leases == nil {
		m.state.Leases = make(map[string]map[string]dhcpLease)
	}
	return nil
}

func (m *dhcpManager) saveLocked() error {
	contents, err := json.MarshalIndent(m.state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode DHCP state: %w", err)
	}
	contents = append(contents, '\n')
	temporaryPath := m.path + ".tmp"
	if err := os.WriteFile(temporaryPath, contents, 0o600); err != nil {
		return fmt.Errorf("write DHCP state: %w", err)
	}
	if err := os.Rename(temporaryPath, m.path); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("replace DHCP state: %w", err)
	}
	return nil
}

func parseDHCPRange(value string) (dhcpRange, error) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
	if err != nil || !prefix.Addr().Is4() {
		return dhcpRange{}, errors.New("DHCP range must be an IPv4 CIDR such as 192.168.100.0/24")
	}
	prefix = prefix.Masked()
	if prefix.Bits() < 8 || prefix.Bits() > 26 {
		return dhcpRange{}, errors.New("DHCP range prefix must be between /8 and /26 so the lease pool can begin at host offset 50")
	}
	network := ipv4ToUint32(prefix.Addr())
	hostBits := uint32(32 - prefix.Bits())
	broadcast := network | (uint32(1)<<hostBits - 1)
	result := dhcpRange{
		Prefix: prefix, Server: uint32ToIPv4(network + 1), PoolStart: uint32ToIPv4(network + dhcpPoolStartOffset),
		PoolEnd: uint32ToIPv4(broadcast - 1), Broadcast: uint32ToIPv4(broadcast),
	}
	if compareIPv4(result.PoolStart, result.PoolEnd) > 0 {
		return dhcpRange{}, errors.New("DHCP range has no usable addresses at or after host offset 50")
	}
	return result, nil
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

func addressInDHCPPool(network dhcpRange, address netip.Addr) bool {
	return address.Is4() && compareIPv4(address, network.PoolStart) >= 0 && compareIPv4(address, network.PoolEnd) <= 0
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
