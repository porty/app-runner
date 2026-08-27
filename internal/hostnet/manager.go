// Package hostnet inspects and mutates the host's Linux bridges and
// interfaces, guarding risky changes with confirmable rollbacks.
package hostnet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/roflware/app-runner/internal/dhcp"
	"github.com/roflware/app-runner/internal/dns"
	"github.com/roflware/app-runner/internal/fsutil"
	"github.com/roflware/app-runner/internal/validate"
	"github.com/roflware/app-runner/internal/vm"
)

const ConfirmationWindow = 15 * time.Second

var (
	ErrBridgeNotFound        = errors.New("bridge not found")
	ErrPendingChangeNotFound = errors.New("pending network change not found")
	ErrChangePending         = errors.New("another network change is waiting for confirmation")
)

type DiagnosticStatus string

const (
	DiagnosticPass    DiagnosticStatus = "pass"
	DiagnosticWarning DiagnosticStatus = "warning"
	DiagnosticFail    DiagnosticStatus = "fail"
	DiagnosticInfo    DiagnosticStatus = "info"
)

type Diagnostic struct {
	Key         string
	Label       string
	Status      DiagnosticStatus
	Detail      string
	Remediation string
}

type UserIdentity struct {
	Username             string
	UID                  uint32
	Groups               []string
	IsRoot               bool
	HasCAPNetAdmin       bool
	HasCAPNetBindService bool
	HasCAPNetRaw         bool
}

type InterfaceInfo struct {
	Name            string
	IsUp            bool
	MTU             uint32
	HardwareAddress string
	Addresses       []string
	Master          string
	IsBridge        bool
	CanAttach       bool
}

type WorkloadAttachment struct {
	ID      string
	Name    string
	Type    string
	Running bool
}

type BridgeInfo struct {
	Name             string
	Description      string
	IsUp             bool
	MTU              uint32
	HardwareAddress  string
	Addresses        []string
	MemberInterfaces []string
	Workloads        []WorkloadAttachment
	Diagnostics      []Diagnostic
	UsableByQEMU     bool
	DHCP             dhcp.BridgeStatus
}

type Status struct {
	User        UserIdentity
	Diagnostics []Diagnostic
	Bridges     []BridgeInfo
	Interfaces  []InterfaceInfo
	Pending     *PendingChange
	CanManage   bool
}

type ChangeType string

const (
	ChangeCreateBridge    ChangeType = "create_bridge"
	ChangeDeleteBridge    ChangeType = "delete_bridge"
	ChangeSetBridgeUp     ChangeType = "set_bridge_up"
	ChangeSetBridgeDown   ChangeType = "set_bridge_down"
	ChangeAttachInterface ChangeType = "attach_interface"
	ChangeDetachInterface ChangeType = "detach_interface"
)

type Change struct {
	Type             ChangeType `json:"type"`
	BridgeName       string     `json:"bridge_name"`
	InterfaceName    string     `json:"interface_name,omitempty"`
	MigrateAddresses bool       `json:"migrate_addresses,omitempty"`
}

type RouteSnapshot struct {
	Destination string `json:"destination,omitempty"`
	Gateway     string `json:"gateway,omitempty"`
	Source      string `json:"source,omitempty"`
	Table       int    `json:"table,omitempty"`
	Priority    int    `json:"priority,omitempty"`
	Scope       int    `json:"scope,omitempty"`
	Protocol    int    `json:"protocol,omitempty"`
	Type        int    `json:"type,omitempty"`
}

type LinkSnapshot struct {
	Name            string          `json:"name"`
	Exists          bool            `json:"exists"`
	IsBridge        bool            `json:"is_bridge,omitempty"`
	IsUp            bool            `json:"is_up,omitempty"`
	MTU             int             `json:"mtu,omitempty"`
	HardwareAddress string          `json:"hardware_address,omitempty"`
	Master          string          `json:"master,omitempty"`
	Addresses       []string        `json:"addresses,omitempty"`
	Routes          []RouteSnapshot `json:"routes,omitempty"`
}

type Snapshot struct {
	Change Change         `json:"change"`
	Links  []LinkSnapshot `json:"links"`
}

type PendingChange struct {
	ID          string    `json:"id"`
	Description string    `json:"description"`
	ExpiresAt   time.Time `json:"expires_at"`
	Snapshot    Snapshot  `json:"snapshot"`
}

type Provider interface {
	Inspect() (Status, error)
	Snapshot(Change) (Snapshot, error)
	Apply(Change) error
	Restore(Snapshot) error
}

type BridgeDescriptionProvider interface {
	SetBridgeDescription(string, string) error
}

type Manager struct {
	mu                 sync.Mutex
	provider           Provider
	vms                VMLister
	statePath          string
	now                func() time.Time
	newID              func() (string, error)
	pending            *PendingChange
	timer              *time.Timer
	confirmationWindow time.Duration
	dhcp               *dhcp.Manager
}

// ManagerOptions wires the network manager to its collaborators.
// VMLister exposes the managed workloads so bridges can report their
// attachments and refuse unsafe changes.
type VMLister interface {
	List() ([]vm.VirtualMachine, error)
}

type ManagerOptions struct {
	Provider       Provider
	VMs            VMLister
	DHCP           *dhcp.Manager
	StateDirectory string
}

func NewManager(options ManagerOptions) (*Manager, error) {
	manager := &Manager{
		provider:           options.Provider,
		vms:                options.VMs,
		dhcp:               options.DHCP,
		statePath:          filepath.Join(options.StateDirectory, "pending-network-change.json"),
		now:                time.Now,
		newID:              fsutil.RandomID,
		confirmationWindow: ConfirmationWindow,
	}
	if err := manager.recoverPendingChange(); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *Manager) Status() (Status, error) {
	status, err := m.provider.Inspect()
	if err != nil {
		return Status{}, err
	}
	vms, err := m.vms.List()
	if err != nil {
		return Status{}, err
	}
	bridgeIndexes := make(map[string]int, len(status.Bridges))
	for index := range status.Bridges {
		bridgeIndexes[status.Bridges[index].Name] = index
	}
	for _, machine := range vms {
		for _, bridge := range machine.Bridges() {
			index, found := bridgeIndexes[bridge]
			if !found {
				status.Bridges = append(status.Bridges, BridgeInfo{
					Name: bridge,
					Diagnostics: []Diagnostic{{
						Key: "bridge_missing", Label: "Bridge exists", Status: DiagnosticFail,
						Detail:      "This bridge is referenced by a managed VM but does not exist on the host.",
						Remediation: fmt.Sprintf("Create bridge `%s` or move the VM to another bridge.", bridge),
					}},
				})
				index = len(status.Bridges) - 1
				bridgeIndexes[bridge] = index
			}
			status.Bridges[index].Workloads = append(status.Bridges[index].Workloads, WorkloadAttachment{
				ID: machine.ID, Name: machine.Name, Type: "virtual_machine",
				Running: machine.Status == vm.StatusRunning || machine.Status == vm.StatusStopping,
			})
		}
	}
	for index := range status.Bridges {
		sort.Slice(status.Bridges[index].Workloads, func(left, right int) bool {
			return status.Bridges[index].Workloads[left].Name < status.Bridges[index].Workloads[right].Name
		})
		if m.dhcp != nil {
			status.Bridges[index].DHCP = m.dhcp.Status(status.Bridges[index].Name)
			for leaseIndex := range status.Bridges[index].DHCP.Leases {
				for _, machine := range vms {
					if machine.HasMACAddress(status.Bridges[index].DHCP.Leases[leaseIndex].HardwareAddress) {
						status.Bridges[index].DHCP.Leases[leaseIndex].VMName = machine.Name
						break
					}
				}
			}
		}
	}
	m.mu.Lock()
	if m.pending != nil {
		pending := *m.pending
		status.Pending = &pending
	}
	m.mu.Unlock()
	return status, nil
}

// Leases lists a bridge's active managed DHCP leases, labelled with the VM
// that owns each MAC address.
func (m *Manager) Leases(bridge string) ([]dhcp.Lease, error) {
	if m.dhcp == nil {
		return nil, errors.New("DHCP manager is not configured")
	}
	status := m.dhcp.Status(bridge)
	if !status.Enabled {
		return nil, fmt.Errorf("%w: DHCP is not enabled on bridge %s", ErrBridgeNotFound, bridge)
	}
	vms, err := m.vms.List()
	if err != nil {
		return nil, err
	}
	for index := range status.Leases {
		for _, machine := range vms {
			if machine.HasMACAddress(status.Leases[index].HardwareAddress) {
				status.Leases[index].VMName = machine.Name
				break
			}
		}
	}
	return status.Leases, nil
}

func (m *Manager) ClearBridgeDHCPLeases(bridge string) error {
	if m.dhcp == nil {
		return errors.New("DHCP manager is not configured")
	}
	return m.dhcp.ClearDynamicLeases(bridge)
}

func (m *Manager) DeleteBridgeDHCPLease(bridge, hardwareAddress string) error {
	if m.dhcp == nil {
		return errors.New("DHCP manager is not configured")
	}
	return m.dhcp.DeleteLease(bridge, hardwareAddress)
}

func (m *Manager) SetBridgeDHCPStaticLease(bridge, hardwareAddress, address string) error {
	if m.dhcp == nil {
		return errors.New("DHCP manager is not configured")
	}
	return m.dhcp.SetStaticLease(bridge, hardwareAddress, address)
}

func (m *Manager) ConfigureBridgeSettings(bridge, description string, enabled bool, cidr string, natEnabled bool, dnsConfig dns.Config) error {
	if m.dhcp == nil {
		return errors.New("DHCP manager is not configured")
	}
	description = strings.TrimSpace(description)
	if len(description) > 255 || strings.ContainsAny(description, "\x00\r\n") {
		return validate.Field("description", "must be a single line of at most 255 characters")
	}
	if err := validate.InterfaceName(bridge); err != nil {
		return validate.Field("bridge_name", err.Error())
	}
	if dnsConfig.Auto {
		vms, err := m.vms.List()
		if err != nil {
			return err
		}
		for _, machine := range vms {
			if !machine.UsesBridge(bridge) {
				continue
			}
			if err := validate.DNSLabel(machine.Name); err != nil {
				return fmt.Errorf("VM %q cannot be published by Auto DNS: %w", machine.Name, err)
			}
		}
	}
	status, err := m.provider.Inspect()
	if err != nil {
		return err
	}
	currentDescription := ""
	found := false
	for _, candidate := range status.Bridges {
		if candidate.Name == bridge {
			currentDescription = candidate.Description
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: %s", ErrBridgeNotFound, bridge)
	}
	descriptionChanged := currentDescription != description
	descriptionProvider, canSetDescription := m.provider.(BridgeDescriptionProvider)
	if descriptionChanged && !canSetDescription {
		return errors.New("bridge descriptions are not supported by the network provider")
	}
	currentDHCP := m.dhcp.Status(bridge)
	servicesChanged := bridgeServicesChanged(currentDHCP, enabled, cidr, natEnabled, dnsConfig)
	if descriptionChanged {
		if err := descriptionProvider.SetBridgeDescription(bridge, description); err != nil {
			return err
		}
	}
	if servicesChanged {
		if err := m.dhcp.Configure(bridge, enabled, cidr, natEnabled, dnsConfig); err != nil {
			if descriptionChanged {
				_ = descriptionProvider.SetBridgeDescription(bridge, currentDescription)
			}
			return err
		}
	}
	return nil
}

func bridgeServicesChanged(current dhcp.BridgeStatus, enabled bool, cidr string, natEnabled bool, dnsConfig dns.Config) bool {
	if current.Enabled != enabled {
		return true
	}
	if !enabled {
		return false
	}
	if current.CIDR != strings.TrimSpace(cidr) || current.NATEnabled != natEnabled || current.DNSEnabled != dnsConfig.Enabled {
		return true
	}
	if !dnsConfig.Enabled {
		return false
	}
	if !slices.Equal(current.DNSForwarders, dnsConfig.Forwarders) || current.AutoDNS != dnsConfig.Auto {
		return true
	}
	return dnsConfig.Auto && current.DNSSuffix != strings.TrimSpace(dnsConfig.Suffix)
}

func (m *Manager) Apply(_ context.Context, change Change) (PendingChange, error) {
	if err := ValidateChange(change); err != nil {
		return PendingChange{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending != nil {
		return PendingChange{}, ErrChangePending
	}
	if err := m.validateWorkloadSafety(change); err != nil {
		return PendingChange{}, err
	}
	snapshot, err := m.provider.Snapshot(change)
	if err != nil {
		return PendingChange{}, err
	}
	id, err := m.newID()
	if err != nil {
		return PendingChange{}, err
	}
	pending := &PendingChange{
		ID: id, Description: describeChange(change),
		ExpiresAt: m.now().Add(m.confirmationWindow), Snapshot: snapshot,
	}
	if err := m.savePending(pending); err != nil {
		return PendingChange{}, err
	}
	m.pending = pending
	if err := m.provider.Apply(change); err != nil {
		restoreErr := m.provider.Restore(snapshot)
		m.clearPendingLocked()
		if restoreErr != nil {
			return PendingChange{}, fmt.Errorf("apply network change: %v; rollback also failed: %w", err, restoreErr)
		}
		return PendingChange{}, err
	}
	m.armTimerLocked(m.confirmationWindow)
	return *pending, nil
}

func (m *Manager) Confirm(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending == nil || m.pending.ID != id {
		return ErrPendingChangeNotFound
	}
	return m.clearPendingLocked()
}

func (m *Manager) Revert(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending == nil || m.pending.ID != id {
		return ErrPendingChangeNotFound
	}
	if err := m.provider.Restore(m.pending.Snapshot); err != nil {
		return err
	}
	return m.clearPendingLocked()
}

func (m *Manager) recoverPendingChange() error {
	contents, err := os.ReadFile(m.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var pending PendingChange
	if err := json.Unmarshal(contents, &pending); err != nil {
		return fmt.Errorf("decode pending network rollback: %w", err)
	}
	if err := m.provider.Restore(pending.Snapshot); err != nil {
		return fmt.Errorf("restore unconfirmed network change after restart: %w", err)
	}
	return fsutil.RemoveIfExists(m.statePath)
}

func (m *Manager) rollbackAfterTimeout(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending == nil || m.pending.ID != id {
		return
	}
	if err := m.provider.Restore(m.pending.Snapshot); err != nil {
		slog.Error("automatic network rollback failed", "change_id", id, "error", err)
		return
	}
	_ = m.clearPendingLocked()
}

func (m *Manager) armTimerLocked(duration time.Duration) {
	id := m.pending.ID
	m.timer = time.AfterFunc(duration, func() { m.rollbackAfterTimeout(id) })
}

func (m *Manager) savePending(pending *PendingChange) error {
	contents, err := json.MarshalIndent(pending, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	return fsutil.WriteFileAtomic(m.statePath, contents, 0o600)
}

func (m *Manager) clearPendingLocked() error {
	if err := fsutil.RemoveIfExists(m.statePath); err != nil {
		return err
	}
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
	m.pending = nil
	return nil
}

func (m *Manager) validateWorkloadSafety(change Change) error {
	if change.Type != ChangeDeleteBridge && change.Type != ChangeSetBridgeDown && change.Type != ChangeDetachInterface {
		return nil
	}
	if change.Type == ChangeDeleteBridge && m.dhcp != nil && m.dhcp.Enabled(change.BridgeName) {
		return fmt.Errorf("disable DHCP on bridge %s before deleting it", change.BridgeName)
	}
	vms, err := m.vms.List()
	if err != nil {
		return err
	}
	for _, machine := range vms {
		if !machine.UsesBridge(change.BridgeName) {
			continue
		}
		if change.Type == ChangeDeleteBridge {
			return fmt.Errorf("bridge %s is assigned to VM %s; move or delete the workload first", change.BridgeName, machine.Name)
		}
		if machine.Status == vm.StatusRunning || machine.Status == vm.StatusStopping {
			return fmt.Errorf("bridge %s is used by running VM %s; stop the workload first", change.BridgeName, machine.Name)
		}
	}
	return nil
}

func ValidateChange(change Change) error {
	if err := validate.InterfaceName(change.BridgeName); err != nil {
		return fmt.Errorf("bridge name %w", err)
	}
	switch change.Type {
	case ChangeCreateBridge, ChangeDeleteBridge, ChangeSetBridgeUp, ChangeSetBridgeDown:
		if change.InterfaceName != "" {
			return errors.New("interface name is not valid for this bridge operation")
		}
	case ChangeAttachInterface, ChangeDetachInterface:
		if err := validate.InterfaceName(change.InterfaceName); err != nil {
			return fmt.Errorf("interface name %w", err)
		}
		if change.InterfaceName == change.BridgeName {
			return errors.New("a bridge cannot be attached to itself")
		}
	default:
		return errors.New("unsupported network change")
	}
	return nil
}

func describeChange(change Change) string {
	switch change.Type {
	case ChangeCreateBridge:
		return "Create bridge " + change.BridgeName
	case ChangeDeleteBridge:
		return "Delete bridge " + change.BridgeName
	case ChangeSetBridgeUp:
		return "Bring bridge " + change.BridgeName + " up"
	case ChangeSetBridgeDown:
		return "Bring bridge " + change.BridgeName + " down"
	case ChangeAttachInterface:
		return fmt.Sprintf("Attach %s to %s", change.InterfaceName, change.BridgeName)
	case ChangeDetachInterface:
		return fmt.Sprintf("Detach %s from %s", change.InterfaceName, change.BridgeName)
	default:
		return strings.ReplaceAll(string(change.Type), "_", " ")
	}
}
