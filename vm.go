package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

type vmStatus string

const (
	vmStatusStopped  vmStatus = "stopped"
	vmStatusRunning  vmStatus = "running"
	vmStatusStopping vmStatus = "stopping"
	vmStatusError    vmStatus = "error"
)

type networkMode string

const (
	networkModeNAT    networkMode = "nat"
	networkModeBridge networkMode = "bridge"
)

var (
	errVMNotFound        = errors.New("virtual machine not found")
	errVMAlreadyRunning  = errors.New("virtual machine is already running")
	errVMNotRunning      = errors.New("virtual machine is not running")
	errVMNameExists      = errors.New("a virtual machine with that name already exists")
	errBridgeUnavailable = errors.New("bridge networking is unavailable")
	errHostUnavailable   = errors.New("required host virtualisation support is unavailable")
	errDeleteRunningVM   = errors.New("stop the virtual machine before deleting it")
)

type fieldError struct {
	field   string
	message string
}

func (e *fieldError) Error() string {
	return fmt.Sprintf("%s: %s", e.field, e.message)
}

type virtualMachine struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	CPUs        uint32      `json:"cpus"`
	MemoryMiB   uint32      `json:"memory_mib"`
	DiskGiB     uint32      `json:"disk_gib"`
	ISOName     string      `json:"iso_name"`
	NetworkMode networkMode `json:"network_mode"`
	BridgeName  string      `json:"bridge_name,omitempty"`
	MACAddress  string      `json:"mac_address,omitempty"`
	Status      vmStatus    `json:"status"`
	CreatedAt   time.Time   `json:"created_at"`
	LastError   string      `json:"last_error,omitempty"`
	PID         int         `json:"pid,omitempty"`
}

type createVMOptions struct {
	Name        string
	CPUs        uint32
	MemoryMiB   uint32
	DiskGiB     uint32
	ISOName     string
	NetworkMode networkMode
	BridgeName  string
}

type isoImage struct {
	Name      string
	SizeBytes uint64
}

type hostCapabilities struct {
	QEMUAvailable   bool
	KVMAvailable    bool
	BridgeAvailable bool
	BridgeName      string
	BridgeWarning   string
}

type hypervisor interface {
	CreateDisk(context.Context, string, uint32) error
	Start(virtualMachine, func(error)) (int, error)
	IsRunning(int) bool
	GracefulStop(virtualMachine) error
	ForceStop(virtualMachine) error
}

type virtualMachineStore interface {
	Load() ([]virtualMachine, error)
	Save([]virtualMachine) error
}

type vmNetworkLifecycle interface {
	Prepare(virtualMachine) error
	Release(virtualMachine)
}

type vmManager struct {
	mu               sync.Mutex
	settings         config
	store            virtualMachineStore
	hypervisor       hypervisor
	capabilities     func() hostCapabilities
	bridgeCapability func(string) (bool, string)
	networkLifecycle vmNetworkLifecycle
	now              func() time.Time
	newID            func() (string, error)
	vms              []virtualMachine
}

func newVMManager(settings config, store virtualMachineStore, hypervisor hypervisor, capabilities func() hostCapabilities) (*vmManager, error) {
	vms, err := store.Load()
	if err != nil {
		return nil, err
	}
	manager := &vmManager{
		settings:     settings,
		store:        store,
		hypervisor:   hypervisor,
		capabilities: capabilities,
		now:          time.Now,
		newID:        randomID,
		vms:          vms,
	}
	manager.bridgeCapability = func(name string) (bool, string) {
		status := capabilities()
		if name == status.BridgeName {
			return status.BridgeAvailable, status.BridgeWarning
		}
		return detectBridgeCapability(name)
	}
	manager.mu.Lock()
	definitionsChanged := false
	for index := range manager.vms {
		if manager.vms[index].NetworkMode == networkModeBridge && manager.vms[index].BridgeName == "" {
			manager.vms[index].BridgeName = settings.BridgeName
			definitionsChanged = true
		}
		if manager.vms[index].MACAddress == "" {
			manager.vms[index].MACAddress = vmMACAddress(manager.vms[index].ID)
			definitionsChanged = true
		}
	}
	err = manager.reconcileLocked()
	if err == nil && definitionsChanged {
		err = manager.store.Save(manager.vms)
	}
	manager.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *vmManager) List() ([]virtualMachine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reconcileLocked(); err != nil {
		return nil, err
	}
	result := slices.Clone(m.vms)
	sort.Slice(result, func(left, right int) bool {
		return result[left].CreatedAt.Before(result[right].CreatedAt)
	})
	return result, nil
}

func (m *vmManager) Get(id string) (virtualMachine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reconcileLocked(); err != nil {
		return virtualMachine{}, err
	}
	index := m.indexByID(id)
	if index == -1 {
		return virtualMachine{}, errVMNotFound
	}
	return m.vms[index], nil
}

func (m *vmManager) Create(ctx context.Context, options createVMOptions) (virtualMachine, error) {
	options.Name = strings.TrimSpace(options.Name)
	if err := m.validateCreateOptions(options); err != nil {
		return virtualMachine{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.vms {
		if strings.EqualFold(existing.Name, options.Name) {
			return virtualMachine{}, errVMNameExists
		}
	}

	id, err := m.newID()
	if err != nil {
		return virtualMachine{}, fmt.Errorf("generate virtual machine ID: %w", err)
	}
	vm := virtualMachine{
		ID:          id,
		Name:        options.Name,
		CPUs:        options.CPUs,
		MemoryMiB:   options.MemoryMiB,
		DiskGiB:     options.DiskGiB,
		ISOName:     options.ISOName,
		NetworkMode: options.NetworkMode,
		BridgeName:  options.BridgeName,
		MACAddress:  vmMACAddress(id),
		Status:      vmStatusStopped,
		CreatedAt:   m.now().UTC(),
	}
	diskPath := m.diskPath(vm.ID)
	if err := m.hypervisor.CreateDisk(ctx, diskPath, vm.DiskGiB); err != nil {
		return virtualMachine{}, fmt.Errorf("create system disk: %w", err)
	}
	m.vms = append(m.vms, vm)
	if err := m.store.Save(m.vms); err != nil {
		_ = os.Remove(diskPath)
		m.vms = m.vms[:len(m.vms)-1]
		return virtualMachine{}, err
	}
	return vm, nil
}

func (m *vmManager) Start(id string) (virtualMachine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reconcileLocked(); err != nil {
		return virtualMachine{}, err
	}
	index := m.indexByID(id)
	if index == -1 {
		return virtualMachine{}, errVMNotFound
	}
	vm := m.vms[index]
	if vm.Status == vmStatusRunning || vm.Status == vmStatusStopping {
		return virtualMachine{}, errVMAlreadyRunning
	}
	capabilities := m.capabilities()
	if !capabilities.QEMUAvailable {
		return virtualMachine{}, fmt.Errorf("%w: QEMU is not available", errHostUnavailable)
	}
	if !capabilities.KVMAvailable {
		return virtualMachine{}, fmt.Errorf("%w: KVM is not available to the current user", errHostUnavailable)
	}
	if vm.NetworkMode == networkModeBridge {
		available, warning := m.bridgeCapability(vm.BridgeName)
		if !available {
			return virtualMachine{}, fmt.Errorf("%w: %s", errBridgeUnavailable, warning)
		}
	}
	if _, err := os.Stat(m.diskPath(vm.ID)); err != nil {
		return virtualMachine{}, fmt.Errorf("system disk is unavailable: %w", err)
	}
	if _, err := os.Stat(m.isoPath(vm.ISOName)); err != nil {
		return virtualMachine{}, fmt.Errorf("installation ISO is unavailable: %w", err)
	}
	if m.networkLifecycle != nil {
		if err := m.networkLifecycle.Prepare(vm); err != nil {
			return virtualMachine{}, fmt.Errorf("%w: %v", errBridgeUnavailable, err)
		}
	}

	pid, err := m.hypervisor.Start(vm, func(exitErr error) { m.processExited(id, exitErr) })
	if err != nil {
		if m.networkLifecycle != nil {
			m.networkLifecycle.Release(vm)
		}
		m.vms[index].Status = vmStatusError
		m.vms[index].LastError = err.Error()
		_ = m.store.Save(m.vms)
		return virtualMachine{}, fmt.Errorf("start virtual machine: %w", err)
	}
	m.vms[index].PID = pid
	m.vms[index].Status = vmStatusRunning
	m.vms[index].LastError = ""
	if err := m.store.Save(m.vms); err != nil {
		_ = m.hypervisor.ForceStop(m.vms[index])
		if m.networkLifecycle != nil {
			m.networkLifecycle.Release(vm)
		}
		return virtualMachine{}, err
	}
	return m.vms[index], nil
}

func (m *vmManager) Stop(id string, force bool) (virtualMachine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reconcileLocked(); err != nil {
		return virtualMachine{}, err
	}
	index := m.indexByID(id)
	if index == -1 {
		return virtualMachine{}, errVMNotFound
	}
	if m.vms[index].Status != vmStatusRunning && m.vms[index].Status != vmStatusStopping {
		return virtualMachine{}, errVMNotRunning
	}

	var err error
	if force {
		err = m.hypervisor.ForceStop(m.vms[index])
		if err == nil {
			stoppedVM := m.vms[index]
			m.vms[index].Status = vmStatusStopped
			m.vms[index].PID = 0
			if m.networkLifecycle != nil {
				m.networkLifecycle.Release(stoppedVM)
			}
		}
	} else {
		err = m.hypervisor.GracefulStop(m.vms[index])
		if err == nil {
			m.vms[index].Status = vmStatusStopping
		}
	}
	if err != nil {
		return virtualMachine{}, err
	}
	if err := m.store.Save(m.vms); err != nil {
		return virtualMachine{}, err
	}
	return m.vms[index], nil
}

func (m *vmManager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reconcileLocked(); err != nil {
		return err
	}
	index := m.indexByID(id)
	if index == -1 {
		return errVMNotFound
	}
	if m.vms[index].Status == vmStatusRunning || m.vms[index].Status == vmStatusStopping {
		return errDeleteRunningVM
	}
	vm := m.vms[index]
	if err := removeIfExists(m.diskPath(vm.ID)); err != nil {
		return fmt.Errorf("remove system disk: %w", err)
	}
	for _, path := range []string{m.qmpSocketPath(vm.ID), m.vncSocketPath(vm.ID)} {
		if err := removeIfExists(path); err != nil {
			return err
		}
	}
	m.vms = append(m.vms[:index], m.vms[index+1:]...)
	return m.store.Save(m.vms)
}

func (m *vmManager) ListISOs() ([]isoImage, error) {
	entries, err := os.ReadDir(m.settings.ISODir)
	if err != nil {
		return nil, err
	}
	images := make([]isoImage, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".iso") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		images = append(images, isoImage{Name: entry.Name(), SizeBytes: uint64(info.Size())})
	}
	sort.Slice(images, func(left, right int) bool {
		return strings.ToLower(images[left].Name) < strings.ToLower(images[right].Name)
	})
	return images, nil
}

func (m *vmManager) ConsoleSocket(id string) (string, error) {
	vm, err := m.Get(id)
	if err != nil {
		return "", err
	}
	if vm.Status != vmStatusRunning && vm.Status != vmStatusStopping {
		return "", errVMNotRunning
	}
	return m.vncSocketPath(id), nil
}

func (m *vmManager) processExited(id string, exitErr error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.indexByID(id)
	if index == -1 {
		return
	}
	if m.vms[index].PID == 0 {
		return
	}
	wasStopping := m.vms[index].Status == vmStatusStopping
	exitedVM := m.vms[index]
	m.vms[index].PID = 0
	m.vms[index].Status = vmStatusStopped
	if exitErr != nil && !wasStopping {
		m.vms[index].LastError = exitErr.Error()
	}
	_ = m.store.Save(m.vms)
	if m.networkLifecycle != nil {
		m.networkLifecycle.Release(exitedVM)
	}
}

func (m *vmManager) reconcileLocked() error {
	changed := false
	for index := range m.vms {
		vm := &m.vms[index]
		if vm.Status != vmStatusRunning && vm.Status != vmStatusStopping {
			continue
		}
		if vm.PID <= 0 || !m.hypervisor.IsRunning(vm.PID) {
			stoppedVM := *vm
			vm.PID = 0
			vm.Status = vmStatusStopped
			if m.networkLifecycle != nil {
				m.networkLifecycle.Release(stoppedVM)
			}
			changed = true
		}
	}
	if changed {
		return m.store.Save(m.vms)
	}
	return nil
}

func (m *vmManager) validateCreateOptions(options createVMOptions) error {
	if options.Name == "" {
		return &fieldError{field: "name", message: "a name is required"}
	}
	if len(options.Name) > 80 {
		return &fieldError{field: "name", message: "must not exceed 80 characters"}
	}
	if options.CPUs < 1 || options.CPUs > 64 {
		return &fieldError{field: "cpus", message: "must be between 1 and 64"}
	}
	if options.MemoryMiB < 256 || options.MemoryMiB > 1_048_576 {
		return &fieldError{field: "memory_mib", message: "must be between 256 and 1048576"}
	}
	if options.DiskGiB < 1 || options.DiskGiB > 2048 {
		return &fieldError{field: "disk_gib", message: "must be between 1 and 2048"}
	}
	if options.NetworkMode != networkModeNAT && options.NetworkMode != networkModeBridge {
		return &fieldError{field: "network_mode", message: "must be NAT or bridge"}
	}
	if options.NetworkMode == networkModeBridge {
		if err := validateInterfaceName(options.BridgeName); err != nil {
			return &fieldError{field: "bridge_name", message: err.Error()}
		}
	}
	if options.ISOName == "" || filepath.Base(options.ISOName) != options.ISOName || !strings.EqualFold(filepath.Ext(options.ISOName), ".iso") {
		return &fieldError{field: "iso_name", message: "must name an ISO file in the configured ISO directory"}
	}
	info, err := os.Stat(m.isoPath(options.ISOName))
	if err != nil || !info.Mode().IsRegular() {
		return &fieldError{field: "iso_name", message: "the selected ISO does not exist"}
	}
	return nil
}

func (m *vmManager) indexByID(id string) int {
	for index := range m.vms {
		if m.vms[index].ID == id {
			return index
		}
	}
	return -1
}

func (m *vmManager) diskPath(id string) string {
	return filepath.Join(m.settings.DiskDir, id+".qcow2")
}

func (m *vmManager) isoPath(name string) string {
	return filepath.Join(m.settings.ISODir, name)
}

func (m *vmManager) qmpSocketPath(id string) string {
	return filepath.Join(m.settings.DiskDir, id+".qmp.sock")
}

func (m *vmManager) vncSocketPath(id string) string {
	return filepath.Join(m.settings.DiskDir, id+".vnc.sock")
}

func randomID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func vmMACAddress(id string) string {
	digest := sha256.Sum256([]byte(id))
	address := net.HardwareAddr{0x52, 0x54, 0x00, digest[0], digest[1], digest[2]}
	return address.String()
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
