package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const networkConfirmationWindow = 15 * time.Second

type diagnosticStatus string

const (
	diagnosticPass    diagnosticStatus = "pass"
	diagnosticWarning diagnosticStatus = "warning"
	diagnosticFail    diagnosticStatus = "fail"
	diagnosticInfo    diagnosticStatus = "info"
)

type networkDiagnostic struct {
	Key         string
	Label       string
	Status      diagnosticStatus
	Detail      string
	Remediation string
}

type userIdentity struct {
	Username       string
	UID            uint32
	Groups         []string
	IsRoot         bool
	HasCAPNetAdmin bool
}

type networkInterfaceInfo struct {
	Name            string
	IsUp            bool
	MTU             uint32
	HardwareAddress string
	Addresses       []string
	Master          string
	IsBridge        bool
	CanAttach       bool
}

type workloadAttachment struct {
	ID      string
	Name    string
	Type    string
	Running bool
}

type networkBridgeInfo struct {
	Name             string
	IsUp             bool
	MTU              uint32
	HardwareAddress  string
	Addresses        []string
	MemberInterfaces []string
	Workloads        []workloadAttachment
	Diagnostics      []networkDiagnostic
	UsableByQEMU     bool
}

type networkingStatus struct {
	User        userIdentity
	Diagnostics []networkDiagnostic
	Bridges     []networkBridgeInfo
	Interfaces  []networkInterfaceInfo
	Pending     *pendingNetworkChange
	CanManage   bool
}

type networkChangeType string

const (
	networkChangeCreateBridge    networkChangeType = "create_bridge"
	networkChangeDeleteBridge    networkChangeType = "delete_bridge"
	networkChangeSetBridgeUp     networkChangeType = "set_bridge_up"
	networkChangeSetBridgeDown   networkChangeType = "set_bridge_down"
	networkChangeAttachInterface networkChangeType = "attach_interface"
	networkChangeDetachInterface networkChangeType = "detach_interface"
)

type networkChange struct {
	Type             networkChangeType `json:"type"`
	BridgeName       string            `json:"bridge_name"`
	InterfaceName    string            `json:"interface_name,omitempty"`
	MigrateAddresses bool              `json:"migrate_addresses,omitempty"`
}

type routeSnapshot struct {
	Destination string `json:"destination,omitempty"`
	Gateway     string `json:"gateway,omitempty"`
	Source      string `json:"source,omitempty"`
	Table       int    `json:"table,omitempty"`
	Priority    int    `json:"priority,omitempty"`
	Scope       int    `json:"scope,omitempty"`
	Protocol    int    `json:"protocol,omitempty"`
	Type        int    `json:"type,omitempty"`
}

type linkSnapshot struct {
	Name            string          `json:"name"`
	Exists          bool            `json:"exists"`
	IsBridge        bool            `json:"is_bridge,omitempty"`
	IsUp            bool            `json:"is_up,omitempty"`
	MTU             int             `json:"mtu,omitempty"`
	HardwareAddress string          `json:"hardware_address,omitempty"`
	Master          string          `json:"master,omitempty"`
	Addresses       []string        `json:"addresses,omitempty"`
	Routes          []routeSnapshot `json:"routes,omitempty"`
}

type networkSnapshot struct {
	Change networkChange  `json:"change"`
	Links  []linkSnapshot `json:"links"`
}

type pendingNetworkChange struct {
	ID          string          `json:"id"`
	Description string          `json:"description"`
	ExpiresAt   time.Time       `json:"expires_at"`
	Snapshot    networkSnapshot `json:"snapshot"`
}

type networkProvider interface {
	Inspect() (networkingStatus, error)
	Snapshot(networkChange) (networkSnapshot, error)
	Apply(networkChange) error
	Restore(networkSnapshot) error
}

type networkManager struct {
	mu                 sync.Mutex
	provider           networkProvider
	vms                *vmManager
	statePath          string
	now                func() time.Time
	newID              func() (string, error)
	pending            *pendingNetworkChange
	timer              *time.Timer
	confirmationWindow time.Duration
}

func newNetworkManager(provider networkProvider, vms *vmManager, diskDirectory string) (*networkManager, error) {
	manager := &networkManager{
		provider:           provider,
		vms:                vms,
		statePath:          filepath.Join(diskDirectory, "pending-network-change.json"),
		now:                time.Now,
		newID:              randomID,
		confirmationWindow: networkConfirmationWindow,
	}
	if err := manager.recoverPendingChange(); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *networkManager) Status() (networkingStatus, error) {
	status, err := m.provider.Inspect()
	if err != nil {
		return networkingStatus{}, err
	}
	vms, err := m.vms.List()
	if err != nil {
		return networkingStatus{}, err
	}
	bridgeIndexes := make(map[string]int, len(status.Bridges))
	for index := range status.Bridges {
		bridgeIndexes[status.Bridges[index].Name] = index
	}
	for _, vm := range vms {
		if vm.NetworkMode != networkModeBridge || vm.BridgeName == "" {
			continue
		}
		index, found := bridgeIndexes[vm.BridgeName]
		if !found {
			status.Bridges = append(status.Bridges, networkBridgeInfo{
				Name: vm.BridgeName,
				Diagnostics: []networkDiagnostic{{
					Key: "bridge_missing", Label: "Bridge exists", Status: diagnosticFail,
					Detail:      "This bridge is referenced by a managed VM but does not exist on the host.",
					Remediation: fmt.Sprintf("Create bridge %s or move the VM to another bridge.", vm.BridgeName),
				}},
			})
			index = len(status.Bridges) - 1
			bridgeIndexes[vm.BridgeName] = index
		}
		status.Bridges[index].Workloads = append(status.Bridges[index].Workloads, workloadAttachment{
			ID: vm.ID, Name: vm.Name, Type: "virtual_machine",
			Running: vm.Status == vmStatusRunning || vm.Status == vmStatusStopping,
		})
	}
	for index := range status.Bridges {
		sort.Slice(status.Bridges[index].Workloads, func(left, right int) bool {
			return status.Bridges[index].Workloads[left].Name < status.Bridges[index].Workloads[right].Name
		})
	}
	m.mu.Lock()
	if m.pending != nil {
		pending := *m.pending
		status.Pending = &pending
	}
	m.mu.Unlock()
	return status, nil
}

func (m *networkManager) Apply(_ context.Context, change networkChange) (pendingNetworkChange, error) {
	if err := validateNetworkChange(change); err != nil {
		return pendingNetworkChange{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending != nil {
		return pendingNetworkChange{}, errors.New("another network change is waiting for confirmation")
	}
	if err := m.validateWorkloadSafety(change); err != nil {
		return pendingNetworkChange{}, err
	}
	snapshot, err := m.provider.Snapshot(change)
	if err != nil {
		return pendingNetworkChange{}, err
	}
	id, err := m.newID()
	if err != nil {
		return pendingNetworkChange{}, err
	}
	pending := &pendingNetworkChange{
		ID: id, Description: describeNetworkChange(change),
		ExpiresAt: m.now().Add(m.confirmationWindow), Snapshot: snapshot,
	}
	if err := m.savePending(pending); err != nil {
		return pendingNetworkChange{}, err
	}
	m.pending = pending
	if err := m.provider.Apply(change); err != nil {
		restoreErr := m.provider.Restore(snapshot)
		m.clearPendingLocked()
		if restoreErr != nil {
			return pendingNetworkChange{}, fmt.Errorf("apply network change: %v; rollback also failed: %w", err, restoreErr)
		}
		return pendingNetworkChange{}, err
	}
	m.armTimerLocked(m.confirmationWindow)
	return *pending, nil
}

func (m *networkManager) Confirm(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending == nil || m.pending.ID != id {
		return errors.New("pending network change not found")
	}
	return m.clearPendingLocked()
}

func (m *networkManager) Revert(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending == nil || m.pending.ID != id {
		return errors.New("pending network change not found")
	}
	if err := m.provider.Restore(m.pending.Snapshot); err != nil {
		return err
	}
	return m.clearPendingLocked()
}

func (m *networkManager) recoverPendingChange() error {
	contents, err := os.ReadFile(m.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var pending pendingNetworkChange
	if err := json.Unmarshal(contents, &pending); err != nil {
		return fmt.Errorf("decode pending network rollback: %w", err)
	}
	if err := m.provider.Restore(pending.Snapshot); err != nil {
		return fmt.Errorf("restore unconfirmed network change after restart: %w", err)
	}
	return removeIfExists(m.statePath)
}

func (m *networkManager) rollbackAfterTimeout(id string) {
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

func (m *networkManager) armTimerLocked(duration time.Duration) {
	id := m.pending.ID
	m.timer = time.AfterFunc(duration, func() { m.rollbackAfterTimeout(id) })
}

func (m *networkManager) savePending(pending *pendingNetworkChange) error {
	contents, err := json.MarshalIndent(pending, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	temporaryPath := m.statePath + ".tmp"
	if err := os.WriteFile(temporaryPath, contents, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, m.statePath); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	return nil
}

func (m *networkManager) clearPendingLocked() error {
	if err := removeIfExists(m.statePath); err != nil {
		return err
	}
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
	m.pending = nil
	return nil
}

func (m *networkManager) validateWorkloadSafety(change networkChange) error {
	if change.Type != networkChangeDeleteBridge && change.Type != networkChangeSetBridgeDown && change.Type != networkChangeDetachInterface {
		return nil
	}
	vms, err := m.vms.List()
	if err != nil {
		return err
	}
	for _, vm := range vms {
		if vm.NetworkMode != networkModeBridge || vm.BridgeName != change.BridgeName {
			continue
		}
		if change.Type == networkChangeDeleteBridge {
			return fmt.Errorf("bridge %s is assigned to VM %s; move or delete the workload first", change.BridgeName, vm.Name)
		}
		if vm.Status == vmStatusRunning || vm.Status == vmStatusStopping {
			return fmt.Errorf("bridge %s is used by running VM %s; stop the workload first", change.BridgeName, vm.Name)
		}
	}
	return nil
}

var interfaceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,14}$`)

func validateInterfaceName(name string) error {
	if !interfaceNamePattern.MatchString(name) {
		return errors.New("must be 1-15 characters using letters, numbers, dot, underscore, or hyphen")
	}
	if name == "lo" {
		return errors.New("loopback cannot be used as a bridge or bridge member")
	}
	return nil
}

func validateNetworkChange(change networkChange) error {
	if err := validateInterfaceName(change.BridgeName); err != nil {
		return fmt.Errorf("bridge name %w", err)
	}
	switch change.Type {
	case networkChangeCreateBridge, networkChangeDeleteBridge, networkChangeSetBridgeUp, networkChangeSetBridgeDown:
		if change.InterfaceName != "" {
			return errors.New("interface name is not valid for this bridge operation")
		}
	case networkChangeAttachInterface, networkChangeDetachInterface:
		if err := validateInterfaceName(change.InterfaceName); err != nil {
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

func describeNetworkChange(change networkChange) string {
	switch change.Type {
	case networkChangeCreateBridge:
		return "Create bridge " + change.BridgeName
	case networkChangeDeleteBridge:
		return "Delete bridge " + change.BridgeName
	case networkChangeSetBridgeUp:
		return "Bring bridge " + change.BridgeName + " up"
	case networkChangeSetBridgeDown:
		return "Bring bridge " + change.BridgeName + " down"
	case networkChangeAttachInterface:
		return fmt.Sprintf("Attach %s to %s", change.InterfaceName, change.BridgeName)
	case networkChangeDetachInterface:
		return fmt.Sprintf("Detach %s from %s", change.InterfaceName, change.BridgeName)
	default:
		return strings.ReplaceAll(string(change.Type), "_", " ")
	}
}
