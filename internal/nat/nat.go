// Package nat manages IPv4 forwarding and the nftables rules that give
// managed bridges outbound connectivity, with recovery across restarts.
package nat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"

	"github.com/roflware/app-runner/internal/fsutil"
)

const TableName = "app_runner_nat"

var firewallRuleTag = []byte("app-runner-managed")

type Network struct {
	Bridge string
	Prefix netip.Prefix
}

type Backend interface {
	IPForwarding() (bool, error)
	SetIPForwarding(bool) error
	ReplaceRules([]Network) error
}

type persistedState struct {
	OriginalIPForwarding bool              `json:"original_ip_forwarding"`
	ChangedIPForwarding  bool              `json:"changed_ip_forwarding"`
	Networks             map[string]string `json:"networks,omitempty"`
}

type Manager struct {
	mu         sync.Mutex
	path       string
	backend    Backend
	state      persistedState
	active     map[string]netip.Prefix
	owned      bool
	recovering bool
}

func NewManager(diskDirectory string) (*Manager, error) {
	return NewManagerWithBackend(filepath.Join(diskDirectory, "nat-runtime.json"), LinuxBackend{})
}

func NewManagerWithBackend(path string, backend Backend) (*Manager, error) {
	manager := &Manager{path: path, backend: backend, active: make(map[string]netip.Prefix)}
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return manager, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read NAT recovery state: %w", err)
	}
	if err := json.Unmarshal(contents, &manager.state); err != nil {
		return nil, fmt.Errorf("decode NAT recovery state: %w", err)
	}
	manager.recovering = true
	manager.owned = true
	return manager, nil
}

func (m *Manager) Start(bridge string, prefix netip.Prefix) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix = prefix.Masked()
	if current, found := m.active[bridge]; found {
		if current != prefix {
			return fmt.Errorf("NAT for bridge %s is already using %s", bridge, current)
		}
		return nil
	}

	first := len(m.active) == 0
	if first && !m.owned {
		forwarding, err := m.backend.IPForwarding()
		if err != nil {
			return fmt.Errorf("read IPv4 forwarding state: %w", err)
		}
		m.state = persistedState{OriginalIPForwarding: forwarding}
		if err := m.saveLocked(); err != nil {
			return err
		}
		m.owned = true
	}
	if first && !m.state.OriginalIPForwarding {
		m.state.ChangedIPForwarding = true
		if err := m.saveLocked(); err != nil {
			return err
		}
		if err := m.backend.SetIPForwarding(true); err != nil {
			return errors.Join(fmt.Errorf("enable IPv4 forwarding: %w", err), m.cleanupLocked())
		}
	}

	m.active[bridge] = prefix
	if err := m.backend.ReplaceRules(sortedNetworks(m.active)); err != nil {
		delete(m.active, bridge)
		var rollbackErr error
		if first {
			rollbackErr = m.cleanupLocked()
		} else {
			rollbackErr = errors.Join(m.backend.ReplaceRules(sortedNetworks(m.active)), m.saveLocked())
		}
		return errors.Join(fmt.Errorf("configure nftables NAT for %s: %w", bridge, err), rollbackErr)
	}
	if err := m.saveLocked(); err != nil {
		delete(m.active, bridge)
		rollbackRulesErr := m.backend.ReplaceRules(sortedNetworks(m.active))
		var rollbackStateErr error
		if first {
			rollbackStateErr = m.cleanupLocked()
		} else {
			rollbackStateErr = m.saveLocked()
		}
		return errors.Join(err, rollbackRulesErr, rollbackStateErr)
	}
	return nil
}

func (m *Manager) Stop(bridge string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix, found := m.active[bridge]
	if !found {
		return nil
	}
	delete(m.active, bridge)
	if err := m.backend.ReplaceRules(sortedNetworks(m.active)); err != nil {
		m.active[bridge] = prefix
		return fmt.Errorf("remove nftables NAT for %s: %w", bridge, err)
	}
	if len(m.active) != 0 {
		return m.saveLocked()
	}
	if err := m.restoreForwardingLocked(); err != nil {
		return errors.Join(err, m.saveLocked())
	}
	return m.removeStateLocked()
}

func (m *Manager) Running(bridge string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, found := m.active[bridge]
	return found
}

func (m *Manager) FinishRecovery() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.recovering {
		return nil
	}
	m.recovering = false
	if len(m.active) != 0 {
		return m.saveLocked()
	}
	return m.cleanupLocked()
}

func (m *Manager) cleanupLocked() error {
	rulesErr := m.backend.ReplaceRules(nil)
	forwardingErr := m.restoreForwardingLocked()
	if cleanupErr := errors.Join(rulesErr, forwardingErr); cleanupErr != nil {
		return errors.Join(cleanupErr, m.saveLocked())
	}
	return m.removeStateLocked()
}

func (m *Manager) removeStateLocked() error {
	if err := fsutil.RemoveIfExists(m.path); err != nil {
		return err
	}
	m.owned = false
	return nil
}

func (m *Manager) restoreForwardingLocked() error {
	if !m.state.ChangedIPForwarding {
		return nil
	}
	if err := m.backend.SetIPForwarding(m.state.OriginalIPForwarding); err != nil {
		return fmt.Errorf("restore IPv4 forwarding: %w", err)
	}
	m.state.ChangedIPForwarding = false
	return nil
}

func (m *Manager) saveLocked() error {
	m.state.Networks = make(map[string]string, len(m.active))
	for bridge, prefix := range m.active {
		m.state.Networks[bridge] = prefix.String()
	}
	contents, err := json.MarshalIndent(m.state, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if err := fsutil.WriteFileAtomic(m.path, contents, 0o600); err != nil {
		return fmt.Errorf("write NAT recovery state: %w", err)
	}
	return nil
}

func sortedNetworks(active map[string]netip.Prefix) []Network {
	networks := make([]Network, 0, len(active))
	for bridge, prefix := range active {
		networks = append(networks, Network{Bridge: bridge, Prefix: prefix.Masked()})
	}
	sort.Slice(networks, func(left, right int) bool { return networks[left].Bridge < networks[right].Bridge })
	return networks
}

type LinuxBackend struct{}

func (LinuxBackend) IPForwarding() (bool, error) {
	contents, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil {
		return false, err
	}
	switch string(contents) {
	case "0\n", "0":
		return false, nil
	case "1\n", "1":
		return true, nil
	default:
		return false, fmt.Errorf("unexpected value %q", string(contents))
	}
}

func (LinuxBackend) SetIPForwarding(enabled bool) error {
	value := []byte("0\n")
	if enabled {
		value = []byte("1\n")
	}
	return os.WriteFile("/proc/sys/net/ipv4/ip_forward", value, 0o644)
}

func (LinuxBackend) ReplaceRules(networks []Network) error {
	connection := &nftables.Conn{}
	tables, err := connection.ListTablesOfFamily(nftables.TableFamilyIPv4)
	if err != nil {
		return err
	}
	for _, table := range tables {
		if table.Name == TableName {
			connection.DelTable(table)
		}
	}
	if err := replaceHostFilterRules(connection, tables, networks); err != nil {
		return err
	}
	if len(networks) != 0 && !hasIPv4FilterTable(tables) {
		slog.Warn("no iptables-nft filter table found; host firewall accept rules for managed NAT bridges were not installed")
	}
	if len(networks) == 0 {
		return connection.Flush()
	}

	table := connection.AddTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: TableName})
	forward := connection.AddChain(&nftables.Chain{
		Name: "forward", Table: table, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookForward, Priority: nftables.ChainPriorityFilter,
	})
	postrouting := connection.AddChain(&nftables.Chain{
		Name: "postrouting", Table: table, Type: nftables.ChainTypeNAT,
		Hooknum: nftables.ChainHookPostrouting, Priority: nftables.ChainPriorityNATSource,
	})
	for _, network := range networks {
		connection.AddRule(&nftables.Rule{Table: table, Chain: forward, Exprs: outboundForwardExpressions(network)})
		connection.AddRule(&nftables.Rule{Table: table, Chain: forward, Exprs: returnForwardExpressions(network)})
		connection.AddRule(&nftables.Rule{Table: table, Chain: postrouting, Exprs: masqueradeExpressions(network)})
	}
	return connection.Flush()
}

func hasIPv4FilterTable(tables []*nftables.Table) bool {
	for _, table := range tables {
		if table.Family == nftables.TableFamilyIPv4 && table.Name == "filter" {
			return true
		}
	}
	return false
}

// replaceHostFilterRules inserts narrowly scoped accepts into an existing
// iptables-nft/UFW filter table. Accepting traffic in App Runner's own base
// chain is insufficient because a later base chain can still drop the packet.
// Tagged rules let us remove only our own entries without altering firewall-
// owned rules or chains.
func replaceHostFilterRules(connection *nftables.Conn, tables []*nftables.Table, networks []Network) error {
	for _, table := range tables {
		if table.Name != "filter" {
			continue
		}
		chains, err := connection.ListChainsOfTableFamily(table.Family)
		if err != nil {
			return fmt.Errorf("list host firewall chains: %w", err)
		}
		for _, chain := range chains {
			if chain.Table == nil || chain.Table.Name != table.Name || (chain.Name != "INPUT" && chain.Name != "FORWARD") {
				continue
			}
			rules, err := connection.GetRules(table, chain)
			if err != nil {
				return fmt.Errorf("list host firewall rules in %s: %w", chain.Name, err)
			}
			for _, rule := range rules {
				if bytes.Equal(rule.UserData, firewallRuleTag) {
					connection.DelRule(rule)
				}
			}
			// InsertRule places every rule at the head. Reverse iteration keeps
			// the resulting order stable and easy to inspect.
			for index := len(networks) - 1; index >= 0; index-- {
				network := networks[index]
				if chain.Name == "INPUT" {
					for _, expressions := range [][]expr.Any{
						serviceInputExpressions(network, unix.IPPROTO_UDP, 67),
						serviceInputExpressions(network, unix.IPPROTO_UDP, 53),
						serviceInputExpressions(network, unix.IPPROTO_TCP, 53),
					} {
						connection.InsertRule(&nftables.Rule{Table: table, Chain: chain, Exprs: expressions, UserData: firewallRuleTag})
					}
				} else {
					connection.InsertRule(&nftables.Rule{Table: table, Chain: chain, Exprs: returnForwardExpressions(network), UserData: firewallRuleTag})
					connection.InsertRule(&nftables.Rule{Table: table, Chain: chain, Exprs: outboundForwardExpressions(network), UserData: firewallRuleTag})
				}
			}
		}
	}
	return nil
}

func serviceInputExpressions(network Network, protocol byte, destinationPort uint16) []expr.Any {
	expressions := interfaceExpressions(expr.MetaKeyIIFNAME, expr.CmpOpEq, network.Bridge)
	expressions = append(expressions,
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{protocol}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(destinationPort)},
		&expr.Verdict{Kind: expr.VerdictAccept},
	)
	return expressions
}

func outboundForwardExpressions(network Network) []expr.Any {
	return append(interfaceExpressions(expr.MetaKeyIIFNAME, expr.CmpOpEq, network.Bridge),
		append(prefixExpressions(12, network.Prefix), &expr.Verdict{Kind: expr.VerdictAccept})...)
}

func returnForwardExpressions(network Network) []expr.Any {
	expressions := interfaceExpressions(expr.MetaKeyOIFNAME, expr.CmpOpEq, network.Bridge)
	expressions = append(expressions, prefixExpressions(16, network.Prefix)...)
	states := expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED
	expressions = append(expressions,
		&expr.Ct{Register: 1, Key: expr.CtKeySTATE},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: binaryutil.NativeEndian.PutUint32(states), Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: []byte{0, 0, 0, 0}},
		&expr.Verdict{Kind: expr.VerdictAccept},
	)
	return expressions
}

func masqueradeExpressions(network Network) []expr.Any {
	expressions := prefixExpressions(12, network.Prefix)
	expressions = append(expressions, interfaceExpressions(expr.MetaKeyOIFNAME, expr.CmpOpNeq, network.Bridge)...)
	return append(expressions, &expr.Masq{})
}

func interfaceExpressions(key expr.MetaKey, operation expr.CmpOp, name string) []expr.Any {
	data := make([]byte, 16)
	copy(data, name+"\x00")
	return []expr.Any{
		&expr.Meta{Key: key, Register: 1},
		&expr.Cmp{Op: operation, Register: 1, Data: data},
	}
}

func prefixExpressions(offset uint32, prefix netip.Prefix) []expr.Any {
	prefix = prefix.Masked()
	address := prefix.Addr().As4()
	mask := net.CIDRMask(prefix.Bits(), 32)
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: 4},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: mask, Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: address[:]},
	}
}
