package apprunner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

type vmStatus string

type vmFirmware string

const (
	vmStatusStopped  vmStatus = "stopped"
	vmStatusRunning  vmStatus = "running"
	vmStatusStopping vmStatus = "stopping"
	vmStatusError    vmStatus = "error"

	vmFirmwareBIOS vmFirmware = "bios"
	vmFirmwareUEFI vmFirmware = "uefi"
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
	errDeviceRunningVM   = errors.New("stop the virtual machine before changing its device configuration")
	errCloneRunningVM    = errors.New("stop the virtual machine before cloning it")
)

type fieldError struct {
	field   string
	message string
}

func (e *fieldError) Error() string {
	return fmt.Sprintf("%s: %s", e.field, e.message)
}

// virtualMachine is the persistent definition of a QEMU virtual machine. A VM
// may have any number of disks, CD-ROM drives, and network interfaces,
// including none; nothing about its devices is implied.
type virtualMachine struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	CPUs        uint32       `json:"cpus"`
	MemoryMiB   uint32       `json:"memory_mib"`
	Firmware    vmFirmware   `json:"firmware"`
	Status      vmStatus     `json:"status"`
	CreatedAt   time.Time    `json:"created_at"`
	LastError   string       `json:"last_error,omitempty"`
	PID         int          `json:"pid,omitempty"`
	Disks       []vmDisk     `json:"disks"`
	CDROMs      []vmCDROM    `json:"cdroms"`
	NICs        []vmNIC      `json:"nics"`
	CloudInit   vmCloudInit  `json:"cloud_init,omitempty"`
	IPMI        vmIPMIConfig `json:"ipmi,omitempty"`
	IPMIRunning bool         `json:"-"`
	IPMIError   string       `json:"-"`
}

// vmDisk is a qcow2 disk image. Disks are attached in list order, so the
// first disk is the one firmware boots from. SourceImage names a cloud image
// inside ISO storage that the disk is a linked overlay of.
type vmDisk struct {
	ID          string `json:"id"`
	SizeGiB     uint32 `json:"size_gib"`
	SourceImage string `json:"source_image,omitempty"`
}

type vmCDROM struct {
	ID      string `json:"id"`
	ISOName string `json:"iso_name,omitempty"`
}

type vmNIC struct {
	ID         string      `json:"id"`
	Mode       networkMode `json:"mode"`
	BridgeName string      `json:"bridge_name,omitempty"`
	MACAddress string      `json:"mac_address"`
}

type vmCloudInit struct {
	Enabled  bool   `json:"enabled,omitempty"`
	UserData string `json:"user_data,omitempty"`
}

type vmIPMIConfig struct {
	Enabled        bool   `json:"enabled,omitempty"`
	BridgeName     string `json:"bridge_name,omitempty"`
	Address        string `json:"address,omitempty"`
	Username       string `json:"username,omitempty"`
	Password       string `json:"password,omitempty"`
	BootDevice     uint8  `json:"boot_device,omitempty"`
	BootPersistent bool   `json:"boot_persistent,omitempty"`
}

// vmDiskSpec, vmCDROMSpec, and vmNICSpec describe devices to add; identities
// are assigned by the manager.
type vmDiskSpec struct {
	SizeGiB     uint32
	SourceImage string
}

type vmCDROMSpec struct {
	ISOName string
}

type vmNICSpec struct {
	Mode       networkMode
	BridgeName string
	MACAddress string
}

type vmIPMIController interface {
	Configure(virtualMachine) error
	Disable(virtualMachine)
	Status(string) (bool, string)
}

type createVMOptions struct {
	Name        string
	Description string
	CPUs        uint32
	MemoryMiB   uint32
	Firmware    vmFirmware
	Disks       []vmDiskSpec
	CDROMs      []vmCDROMSpec
	NICs        []vmNICSpec
	CloudInit   vmCloudInit
}

type isoImage struct {
	Name      string
	SizeBytes uint64
}

type diskImage struct {
	Name      string
	SizeBytes uint64
}

type hostCapabilities struct {
	QEMUAvailable bool
	KVMAvailable  bool
}

type hypervisor interface {
	CreateDisk(context.Context, string, uint32) error
	CreateLinkedDisk(context.Context, string, string, uint32) error
	Start(virtualMachine, func(int, error)) (int, error)
	IsRunning(int) bool
	GracefulStop(virtualMachine) error
	ForceStop(virtualMachine) error
	Reset(virtualMachine) error
	ChangeCDROMMedia(virtualMachine, string, string) error
}

type virtualMachineStore interface {
	Load() ([]virtualMachine, error)
	Save([]virtualMachine) error
}

type vmNetworkLifecycle interface {
	Prepare(virtualMachine) error
	Release(virtualMachine)
}

// vmManagerOptions wires the VM manager to the host facilities it drives.
type vmManagerOptions struct {
	Settings         config
	Store            virtualMachineStore
	Hypervisor       hypervisor
	Capabilities     func() hostCapabilities
	BridgeCapability func(string) (bool, string)
}

type vmManager struct {
	mu                sync.Mutex
	settings          config
	store             virtualMachineStore
	hypervisor        hypervisor
	capabilities      func() hostCapabilities
	bridgeCapability  func(string) (bool, string)
	networkLifecycle  vmNetworkLifecycle
	ipmi              vmIPMIController
	now               func() time.Time
	newID             func() (string, error)
	buildCloudInitISO cloudInitISOBuilder
	vms               []virtualMachine
	// exits holds, per running VM, a channel closed once its process exit
	// has been observed and its runtime files cleaned up.
	exits            map[string]chan struct{}
	forceStopTimeout time.Duration
}

// defaultForceStopTimeout bounds how long a forced stop waits for the killed
// process to be reaped before reporting the VM stopped anyway.
const defaultForceStopTimeout = 10 * time.Second

func newVMManager(options vmManagerOptions) (*vmManager, error) {
	vms, err := options.Store.Load()
	if err != nil {
		return nil, err
	}
	settings := options.Settings
	manager := &vmManager{
		settings:         settings,
		store:            options.Store,
		hypervisor:       options.Hypervisor,
		capabilities:     options.Capabilities,
		bridgeCapability: options.BridgeCapability,
		now:              time.Now,
		newID:            randomID,
		exits:            make(map[string]chan struct{}),
		forceStopTimeout: defaultForceStopTimeout,
		buildCloudInitISO: func(ctx context.Context, destination string, options cloudInitISOOptions) error {
			return createCloudInitISO(ctx, settings.XorrisoBinary, destination, options)
		},
		vms: vms,
	}
	if manager.bridgeCapability == nil {
		manager.bridgeCapability = func(string) (bool, string) { return false, "bridge networking is not configured" }
	}
	if err := manager.validateStoredDefinitions(); err != nil {
		return nil, err
	}
	manager.mu.Lock()
	err = manager.reconcileLocked()
	manager.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *vmManager) validateStoredDefinitions() error {
	for index := range m.vms {
		vm := &m.vms[index]
		if vm.ID == "" {
			return fmt.Errorf("stored virtual machine %q has no ID", vm.Name)
		}
		if vm.Firmware == "" {
			vm.Firmware = vmFirmwareBIOS
		}
		for _, disk := range vm.Disks {
			if disk.ID == "" {
				return fmt.Errorf("virtual machine %s has a disk without an ID", vm.Name)
			}
		}
		for _, nic := range vm.NICs {
			if nic.ID == "" || nic.MACAddress == "" {
				return fmt.Errorf("virtual machine %s has a network interface without an ID or MAC address", vm.Name)
			}
		}
	}
	return nil
}

func (m *vmManager) List() ([]virtualMachine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reconcileLocked(); err != nil {
		return nil, err
	}
	result := make([]virtualMachine, 0, len(m.vms))
	for _, vm := range m.vms {
		result = append(result, cloneVirtualMachine(vm))
	}
	if m.ipmi != nil {
		for index := range result {
			result[index].IPMIRunning, result[index].IPMIError = m.ipmi.Status(result[index].ID)
		}
	}
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
	result := cloneVirtualMachine(m.vms[index])
	if m.ipmi != nil {
		result.IPMIRunning, result.IPMIError = m.ipmi.Status(result.ID)
	}
	return result, nil
}

// cloneVirtualMachine copies a definition so callers cannot alias the
// manager's device slices.
func cloneVirtualMachine(vm virtualMachine) virtualMachine {
	vm.Disks = slices.Clone(vm.Disks)
	vm.CDROMs = slices.Clone(vm.CDROMs)
	vm.NICs = slices.Clone(vm.NICs)
	return vm
}

func (m *vmManager) Create(ctx context.Context, options createVMOptions) (virtualMachine, error) {
	options.Name = strings.TrimSpace(options.Name)
	options.Description = strings.TrimSpace(options.Description)
	if err := validateVMIdentityAndResources(options.Name, options.Description, options.CPUs, options.MemoryMiB); err != nil {
		return virtualMachine{}, err
	}
	for index := range options.Disks {
		options.Disks[index].SourceImage = normalizeStorageName(options.Disks[index].SourceImage)
		if err := m.validateDiskSpec(options.Disks[index]); err != nil {
			return virtualMachine{}, err
		}
	}
	for index := range options.CDROMs {
		options.CDROMs[index].ISOName = normalizeStorageName(options.CDROMs[index].ISOName)
		if err := m.validateOptionalISO(options.CDROMs[index].ISOName); err != nil {
			return virtualMachine{}, err
		}
	}
	for index := range options.NICs {
		normalized, err := normalizeNICSpec(options.NICs[index])
		if err != nil {
			return virtualMachine{}, err
		}
		options.NICs[index] = normalized
	}
	if err := validateCloudInit(options.CloudInit); err != nil {
		return virtualMachine{}, err
	}
	if !options.CloudInit.Enabled {
		options.CloudInit = vmCloudInit{}
	}
	firmware := options.Firmware
	if firmware == "" {
		firmware = vmFirmwareBIOS
		for _, disk := range options.Disks {
			if disk.SourceImage != "" {
				firmware = vmFirmwareUEFI
			}
		}
	}
	if firmware != vmFirmwareBIOS && firmware != vmFirmwareUEFI {
		return virtualMachine{}, &fieldError{field: "firmware", message: "must be bios or uefi"}
	}
	if firmware == vmFirmwareUEFI && (!regularFile(m.settings.UEFICodePath) || !regularFile(m.settings.UEFIVarsPath)) {
		return virtualMachine{}, &fieldError{field: "firmware", message: "UEFI firmware is unavailable; configure uefi_code_path and uefi_vars_path"}
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
		Description: options.Description,
		CPUs:        options.CPUs,
		MemoryMiB:   options.MemoryMiB,
		Firmware:    firmware,
		Status:      vmStatusStopped,
		CreatedAt:   m.now().UTC(),
		Disks:       make([]vmDisk, 0, len(options.Disks)),
		CDROMs:      make([]vmCDROM, 0, len(options.CDROMs)),
		NICs:        make([]vmNIC, 0, len(options.NICs)),
		CloudInit:   options.CloudInit,
	}
	for _, spec := range options.Disks {
		diskID, err := randomID()
		if err != nil {
			return virtualMachine{}, fmt.Errorf("generate disk ID: %w", err)
		}
		vm.Disks = append(vm.Disks, vmDisk{ID: diskID, SizeGiB: spec.SizeGiB, SourceImage: spec.SourceImage})
	}
	for _, spec := range options.CDROMs {
		cdromID, err := randomID()
		if err != nil {
			return virtualMachine{}, fmt.Errorf("generate CD-ROM ID: %w", err)
		}
		vm.CDROMs = append(vm.CDROMs, vmCDROM{ID: cdromID, ISOName: spec.ISOName})
	}
	for _, spec := range options.NICs {
		nic, err := m.newNICLocked(vm, spec)
		if err != nil {
			return virtualMachine{}, err
		}
		vm.NICs = append(vm.NICs, nic)
	}

	created := make([]string, 0, len(vm.Disks))
	cleanup := func() {
		for _, path := range created {
			_ = removeIfExists(path)
		}
		_ = os.RemoveAll(m.settings.vmStateDir(vm.ID))
	}
	for _, disk := range vm.Disks {
		if err := m.createDiskFile(ctx, vm.ID, disk); err != nil {
			cleanup()
			return virtualMachine{}, err
		}
		created = append(created, m.vmDiskPath(vm.ID, disk))
	}
	if err := os.MkdirAll(m.settings.vmStateDir(vm.ID), 0o750); err != nil {
		cleanup()
		return virtualMachine{}, fmt.Errorf("create VM state directory: %w", err)
	}
	if vm.Firmware == vmFirmwareUEFI {
		if err := copyVMFile(ctx, m.settings.UEFIVarsPath, m.uefiVarsPath(vm.ID)); err != nil {
			cleanup()
			return virtualMachine{}, fmt.Errorf("create UEFI variable store: %w", err)
		}
	}
	if vm.CloudInit.Enabled {
		if err := m.buildCloudInitISO(ctx, m.cloudInitISOPath(vm.ID), cloudInitOptions(vm)); err != nil {
			cleanup()
			return virtualMachine{}, err
		}
	}
	m.vms = append(m.vms, vm)
	if err := m.store.Save(m.vms); err != nil {
		m.vms = m.vms[:len(m.vms)-1]
		cleanup()
		return virtualMachine{}, err
	}
	return cloneVirtualMachine(vm), nil
}

func (m *vmManager) createDiskFile(ctx context.Context, vmID string, disk vmDisk) error {
	path := m.vmDiskPath(vmID, disk)
	if disk.SourceImage == "" {
		if err := m.hypervisor.CreateDisk(ctx, path, disk.SizeGiB); err != nil {
			return fmt.Errorf("create disk: %w", err)
		}
		return nil
	}
	sourcePath, err := m.resolveISOStorageFile(disk.SourceImage, ".img", ".qcow2")
	if err != nil {
		return &fieldError{field: "source_image", message: err.Error()}
	}
	if err := m.hypervisor.CreateLinkedDisk(ctx, path, sourcePath, disk.SizeGiB); err != nil {
		return fmt.Errorf("create linked disk: %w", err)
	}
	return nil
}

func (m *vmManager) Clone(ctx context.Context, id, name string) (virtualMachine, error) {
	name = strings.TrimSpace(name)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reconcileLocked(); err != nil {
		return virtualMachine{}, err
	}
	index := m.indexByID(id)
	if index == -1 {
		return virtualMachine{}, errVMNotFound
	}
	source := m.vms[index]
	if source.Status != vmStatusStopped {
		return virtualMachine{}, errCloneRunningVM
	}
	if err := validateVMIdentityAndResources(name, source.Description, source.CPUs, source.MemoryMiB); err != nil {
		return virtualMachine{}, err
	}
	for _, existing := range m.vms {
		if strings.EqualFold(existing.Name, name) {
			return virtualMachine{}, errVMNameExists
		}
	}

	cloneID, err := m.newID()
	if err != nil {
		return virtualMachine{}, fmt.Errorf("generate cloned virtual machine ID: %w", err)
	}
	if m.indexByID(cloneID) != -1 {
		return virtualMachine{}, errors.New("generated cloned virtual machine ID already exists")
	}

	clone := virtualMachine{
		ID:          cloneID,
		Name:        name,
		Description: source.Description,
		CPUs:        source.CPUs,
		MemoryMiB:   source.MemoryMiB,
		Firmware:    source.Firmware,
		Status:      vmStatusStopped,
		CreatedAt:   m.now().UTC(),
		Disks:       make([]vmDisk, 0, len(source.Disks)),
		CDROMs:      make([]vmCDROM, 0, len(source.CDROMs)),
		NICs:        make([]vmNIC, 0, len(source.NICs)),
		CloudInit:   source.CloudInit,
	}
	for _, sourceDisk := range source.Disks {
		clonedDisk := sourceDisk
		clonedDisk.ID, err = randomID()
		if err != nil {
			return virtualMachine{}, fmt.Errorf("generate cloned disk ID: %w", err)
		}
		clone.Disks = append(clone.Disks, clonedDisk)
	}
	for _, sourceCDROM := range source.CDROMs {
		cdromID, err := randomID()
		if err != nil {
			return virtualMachine{}, fmt.Errorf("generate cloned CD-ROM ID: %w", err)
		}
		clone.CDROMs = append(clone.CDROMs, vmCDROM{ID: cdromID, ISOName: sourceCDROM.ISOName})
	}
	for _, sourceNIC := range source.NICs {
		nic, err := m.newNICLocked(clone, vmNICSpec{Mode: sourceNIC.Mode, BridgeName: sourceNIC.BridgeName})
		if err != nil {
			return virtualMachine{}, err
		}
		clone.NICs = append(clone.NICs, nic)
	}

	if err := os.MkdirAll(m.settings.vmStateDir(clone.ID), 0o750); err != nil {
		return virtualMachine{}, fmt.Errorf("create VM state directory: %w", err)
	}
	createdPaths := make([]string, 0, len(clone.Disks))
	removeCreated := func() {
		for _, path := range createdPaths {
			_ = removeIfExists(path)
		}
		_ = os.RemoveAll(m.settings.vmStateDir(clone.ID))
	}
	for diskIndex, sourceDisk := range source.Disks {
		destinationPath := m.vmDiskPath(clone.ID, clone.Disks[diskIndex])
		if err := copyVMFile(ctx, m.vmDiskPath(source.ID, sourceDisk), destinationPath); err != nil {
			removeCreated()
			return virtualMachine{}, fmt.Errorf("copy disk %s: %w", sourceDisk.ID, err)
		}
		createdPaths = append(createdPaths, destinationPath)
	}
	if clone.CloudInit.Enabled {
		if err := m.buildCloudInitISO(ctx, m.cloudInitISOPath(clone.ID), cloudInitOptions(clone)); err != nil {
			removeCreated()
			return virtualMachine{}, err
		}
	}
	if clone.Firmware == vmFirmwareUEFI {
		if err := copyVMFile(ctx, m.uefiVarsPath(source.ID), m.uefiVarsPath(clone.ID)); err != nil {
			removeCreated()
			return virtualMachine{}, fmt.Errorf("clone UEFI variable store: %w", err)
		}
	}

	m.vms = append(m.vms, clone)
	if err := m.store.Save(m.vms); err != nil {
		m.vms = m.vms[:len(m.vms)-1]
		removeCreated()
		return virtualMachine{}, err
	}
	return cloneVirtualMachine(clone), nil
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
	for _, bridge := range vm.bridges() {
		available, warning := m.bridgeCapability(bridge)
		if !available {
			return virtualMachine{}, fmt.Errorf("%w: %s", errBridgeUnavailable, warning)
		}
	}
	for _, disk := range vm.Disks {
		if _, err := os.Stat(m.vmDiskPath(vm.ID, disk)); err != nil {
			return virtualMachine{}, fmt.Errorf("disk %s is unavailable: %w", disk.ID, err)
		}
		if disk.SourceImage != "" {
			if _, err := m.resolveISOStorageFile(disk.SourceImage, ".img", ".qcow2"); err != nil {
				return virtualMachine{}, fmt.Errorf("cloud image %s for disk %s is unavailable: %w", disk.SourceImage, disk.ID, err)
			}
		}
	}
	for _, cdrom := range vm.CDROMs {
		if cdrom.ISOName == "" {
			continue
		}
		if _, err := os.Stat(m.isoPath(cdrom.ISOName)); err != nil {
			return virtualMachine{}, fmt.Errorf("CD-ROM ISO %s is unavailable: %w", cdrom.ISOName, err)
		}
	}
	if vm.CloudInit.Enabled {
		if _, err := os.Stat(m.cloudInitISOPath(vm.ID)); err != nil {
			return virtualMachine{}, fmt.Errorf("cloud-init ISO is unavailable: %w", err)
		}
	}
	if vm.Firmware == vmFirmwareUEFI {
		for label, path := range map[string]string{
			"UEFI code image":     m.settings.UEFICodePath,
			"UEFI variable store": m.uefiVarsPath(vm.ID),
		} {
			if !regularFile(path) {
				return virtualMachine{}, fmt.Errorf("%s is unavailable: %s", label, path)
			}
		}
	}
	if m.networkLifecycle != nil {
		if err := m.networkLifecycle.Prepare(vm); err != nil {
			return virtualMachine{}, fmt.Errorf("%w: %v", errBridgeUnavailable, err)
		}
	}

	exited := make(chan struct{})
	m.exits[id] = exited
	pid, err := m.hypervisor.Start(vm, func(exitedPID int, exitErr error) { m.processExited(id, exitedPID, exitErr) })
	if err != nil {
		delete(m.exits, id)
		if m.networkLifecycle != nil {
			m.networkLifecycle.Release(vm)
		}
		m.vms[index].Status = vmStatusError
		m.vms[index].LastError = err.Error()
		m.saveLocked()
		return virtualMachine{}, fmt.Errorf("start virtual machine: %w", err)
	}
	m.vms[index].PID = pid
	m.vms[index].Status = vmStatusRunning
	m.vms[index].LastError = ""
	if m.vms[index].IPMI.BootDevice != 0 && !m.vms[index].IPMI.BootPersistent {
		m.vms[index].IPMI.BootDevice = 0
	}
	if err := m.store.Save(m.vms); err != nil {
		_ = m.hypervisor.ForceStop(m.vms[index])
		if m.networkLifecycle != nil {
			m.networkLifecycle.Release(vm)
		}
		return virtualMachine{}, err
	}
	return cloneVirtualMachine(m.vms[index]), nil
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

	if force {
		return m.forceStopLocked(index)
	}
	if err := m.hypervisor.GracefulStop(m.vms[index]); err != nil {
		return virtualMachine{}, err
	}
	m.vms[index].Status = vmStatusStopping
	if err := m.store.Save(m.vms); err != nil {
		return virtualMachine{}, err
	}
	return cloneVirtualMachine(m.vms[index]), nil
}

// forceStopLocked kills the VM process and waits, with the manager unlocked,
// until its exit has been processed so that runtime sockets are gone and a
// following Start cannot race the dying process. The lock is held on entry
// and on return.
func (m *vmManager) forceStopLocked(index int) (virtualMachine, error) {
	id := m.vms[index].ID
	if err := m.hypervisor.ForceStop(m.vms[index]); err != nil {
		return virtualMachine{}, err
	}
	m.vms[index].Status = vmStatusStopping
	if err := m.store.Save(m.vms); err != nil {
		return virtualMachine{}, err
	}
	exited := m.exits[id]
	m.mu.Unlock()
	if exited != nil {
		select {
		case <-exited:
		case <-time.After(m.forceStopTimeout):
		}
	}
	m.mu.Lock()
	index = m.indexByID(id)
	if index == -1 {
		return virtualMachine{}, errVMNotFound
	}
	if m.vms[index].Status != vmStatusStopped {
		stoppedVM := m.vms[index]
		m.vms[index].Status = vmStatusStopped
		m.vms[index].PID = 0
		m.signalExitLocked(id)
		if m.networkLifecycle != nil {
			m.networkLifecycle.Release(stoppedVM)
		}
		if err := m.store.Save(m.vms); err != nil {
			return virtualMachine{}, err
		}
	}
	return cloneVirtualMachine(m.vms[index]), nil
}

// signalExitLocked wakes anything waiting for the VM's process to exit.
func (m *vmManager) signalExitLocked(id string) {
	if exited := m.exits[id]; exited != nil {
		close(exited)
		delete(m.exits, id)
	}
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
	if m.ipmi != nil {
		m.ipmi.Disable(vm)
	}
	for _, disk := range vm.Disks {
		if err := removeIfExists(m.vmDiskPath(vm.ID, disk)); err != nil {
			return fmt.Errorf("remove disk %s: %w", disk.ID, err)
		}
	}
	for _, path := range []string{m.qmpSocketPath(vm.ID), m.vncSocketPath(vm.ID)} {
		if err := removeIfExists(path); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(m.settings.vmStateDir(vm.ID)); err != nil {
		return fmt.Errorf("remove VM state: %w", err)
	}
	m.vms = append(m.vms[:index], m.vms[index+1:]...)
	return m.store.Save(m.vms)
}

func (m *vmManager) Update(id, name, description string, cpus, memoryMiB uint32) (virtualMachine, error) {
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if err := validateVMIdentityAndResources(name, description, cpus, memoryMiB); err != nil {
		return virtualMachine{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reconcileLocked(); err != nil {
		return virtualMachine{}, err
	}
	index := m.indexByID(id)
	if index == -1 {
		return virtualMachine{}, errVMNotFound
	}
	for candidateIndex, candidate := range m.vms {
		if candidateIndex != index && strings.EqualFold(candidate.Name, name) {
			return virtualMachine{}, errVMNameExists
		}
	}
	previous := m.vms[index]
	m.vms[index].Name = name
	m.vms[index].Description = description
	m.vms[index].CPUs = cpus
	m.vms[index].MemoryMiB = memoryMiB
	commitCloudInit := func() {}
	rollbackCloudInit := func() {}
	if previous.Name != name && m.vms[index].CloudInit.Enabled {
		var stageErr error
		commitCloudInit, rollbackCloudInit, stageErr = m.stageCloudInitISO(context.Background(), m.vms[index])
		if stageErr != nil {
			m.vms[index] = previous
			return virtualMachine{}, stageErr
		}
	}
	if err := m.store.Save(m.vms); err != nil {
		rollbackCloudInit()
		m.vms[index] = previous
		return virtualMachine{}, err
	}
	commitCloudInit()
	return cloneVirtualMachine(m.vms[index]), nil
}

func (m *vmManager) ConfigureCloudInit(ctx context.Context, id string, configuration vmCloudInit) (virtualMachine, error) {
	if err := validateCloudInit(configuration); err != nil {
		return virtualMachine{}, err
	}
	if !configuration.Enabled {
		configuration = vmCloudInit{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reconcileLocked(); err != nil {
		return virtualMachine{}, err
	}
	index := m.indexByID(id)
	if index == -1 {
		return virtualMachine{}, errVMNotFound
	}
	previous := m.vms[index]
	if previous.CloudInit == configuration {
		return cloneVirtualMachine(previous), nil
	}
	if vmIsActive(previous) {
		return virtualMachine{}, errDeviceRunningVM
	}
	candidate := previous
	candidate.CloudInit = configuration
	commit, rollback, err := m.stageCloudInitISO(ctx, candidate)
	if err != nil {
		return virtualMachine{}, err
	}
	m.vms[index] = candidate
	if err := m.store.Save(m.vms); err != nil {
		m.vms[index] = previous
		rollback()
		return virtualMachine{}, err
	}
	commit()
	return cloneVirtualMachine(candidate), nil
}

// AddNIC attaches a network interface to a stopped VM.
func (m *vmManager) AddNIC(id string, spec vmNICSpec) (virtualMachine, error) {
	spec, err := normalizeNICSpec(spec)
	if err != nil {
		return virtualMachine{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reconcileLocked(); err != nil {
		return virtualMachine{}, err
	}
	index := m.indexByID(id)
	if index == -1 {
		return virtualMachine{}, errVMNotFound
	}
	if vmIsActive(m.vms[index]) {
		return virtualMachine{}, errDeviceRunningVM
	}
	nic, err := m.newNICLocked(m.vms[index], spec)
	if err != nil {
		return virtualMachine{}, err
	}
	previous := slices.Clone(m.vms[index].NICs)
	m.vms[index].NICs = append(m.vms[index].NICs, nic)
	if err := m.store.Save(m.vms); err != nil {
		m.vms[index].NICs = previous
		return virtualMachine{}, err
	}
	return cloneVirtualMachine(m.vms[index]), nil
}

// UpdateNIC changes a network interface. Saving an unchanged interface is
// allowed while the VM runs; any real change requires it to be stopped.
func (m *vmManager) UpdateNIC(id, nicID string, spec vmNICSpec) (virtualMachine, error) {
	spec, err := normalizeNICSpec(spec)
	if err != nil {
		return virtualMachine{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reconcileLocked(); err != nil {
		return virtualMachine{}, err
	}
	index := m.indexByID(id)
	if index == -1 {
		return virtualMachine{}, errVMNotFound
	}
	nicIndex := slices.IndexFunc(m.vms[index].NICs, func(nic vmNIC) bool { return nic.ID == nicID })
	if nicIndex == -1 {
		return virtualMachine{}, &fieldError{field: "nic_id", message: "network interface was not found"}
	}
	current := m.vms[index].NICs[nicIndex]
	updated := vmNIC{ID: current.ID, Mode: spec.Mode, BridgeName: spec.BridgeName, MACAddress: spec.MACAddress}
	if updated.MACAddress == "" {
		updated.MACAddress = current.MACAddress
	}
	if updated == current {
		return cloneVirtualMachine(m.vms[index]), nil
	}
	if vmIsActive(m.vms[index]) {
		return virtualMachine{}, errDeviceRunningVM
	}
	if !strings.EqualFold(updated.MACAddress, current.MACAddress) && m.macAddressInUseLocked(updated.MACAddress) {
		return virtualMachine{}, &fieldError{field: "mac_address", message: "is already assigned to another network interface"}
	}
	previous := slices.Clone(m.vms[index].NICs)
	m.vms[index].NICs = slices.Clone(m.vms[index].NICs)
	m.vms[index].NICs[nicIndex] = updated
	if err := m.store.Save(m.vms); err != nil {
		m.vms[index].NICs = previous
		return virtualMachine{}, err
	}
	return cloneVirtualMachine(m.vms[index]), nil
}

func (m *vmManager) RemoveNIC(id, nicID string) (virtualMachine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reconcileLocked(); err != nil {
		return virtualMachine{}, err
	}
	index := m.indexByID(id)
	if index == -1 {
		return virtualMachine{}, errVMNotFound
	}
	if vmIsActive(m.vms[index]) {
		return virtualMachine{}, errDeviceRunningVM
	}
	nicIndex := slices.IndexFunc(m.vms[index].NICs, func(nic vmNIC) bool { return nic.ID == nicID })
	if nicIndex == -1 {
		return virtualMachine{}, &fieldError{field: "nic_id", message: "network interface was not found"}
	}
	previous := slices.Clone(m.vms[index].NICs)
	m.vms[index].NICs = slices.Delete(slices.Clone(m.vms[index].NICs), nicIndex, nicIndex+1)
	if err := m.store.Save(m.vms); err != nil {
		m.vms[index].NICs = previous
		return virtualMachine{}, err
	}
	return cloneVirtualMachine(m.vms[index]), nil
}

// newNICLocked allocates an interface identity for a spec, generating a
// stable MAC address when none is requested.
func (m *vmManager) newNICLocked(vm virtualMachine, spec vmNICSpec) (vmNIC, error) {
	nicID, err := randomID()
	if err != nil {
		return vmNIC{}, fmt.Errorf("generate network interface ID: %w", err)
	}
	nic := vmNIC{ID: nicID, Mode: spec.Mode, BridgeName: spec.BridgeName, MACAddress: spec.MACAddress}
	if nic.MACAddress == "" {
		nic.MACAddress = nicMACAddress(vm.ID, nicID)
	}
	if m.macAddressInUseLocked(nic.MACAddress) || vm.hasMACAddress(nic.MACAddress) {
		return vmNIC{}, &fieldError{field: "mac_address", message: "is already assigned to another network interface"}
	}
	return nic, nil
}

func (m *vmManager) macAddressInUseLocked(macAddress string) bool {
	for _, vm := range m.vms {
		if vm.hasMACAddress(macAddress) {
			return true
		}
	}
	return false
}

func normalizeNICSpec(spec vmNICSpec) (vmNICSpec, error) {
	spec.BridgeName = strings.TrimSpace(spec.BridgeName)
	spec.MACAddress = strings.TrimSpace(spec.MACAddress)
	switch spec.Mode {
	case networkModeNAT:
		spec.BridgeName = ""
	case networkModeBridge:
		if err := validateInterfaceName(spec.BridgeName); err != nil {
			return vmNICSpec{}, &fieldError{field: "bridge_name", message: err.Error()}
		}
	default:
		return vmNICSpec{}, &fieldError{field: "mode", message: "must be NAT or bridge"}
	}
	if spec.MACAddress != "" {
		parsed, err := net.ParseMAC(spec.MACAddress)
		if err != nil || len(parsed) != 6 || parsed[0]&1 != 0 {
			return vmNICSpec{}, &fieldError{field: "mac_address", message: "must be a valid unicast MAC address"}
		}
		spec.MACAddress = parsed.String()
	}
	return spec, nil
}

func (m *vmManager) AddDisk(ctx context.Context, id string, spec vmDiskSpec) (virtualMachine, error) {
	spec.SourceImage = normalizeStorageName(spec.SourceImage)
	if err := m.validateDiskSpec(spec); err != nil {
		return virtualMachine{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.indexByID(id)
	if index == -1 {
		return virtualMachine{}, errVMNotFound
	}
	if vmIsActive(m.vms[index]) {
		return virtualMachine{}, errDeviceRunningVM
	}
	diskID, err := randomID()
	if err != nil {
		return virtualMachine{}, fmt.Errorf("generate disk ID: %w", err)
	}
	disk := vmDisk{ID: diskID, SizeGiB: spec.SizeGiB, SourceImage: spec.SourceImage}
	if err := m.createDiskFile(ctx, id, disk); err != nil {
		return virtualMachine{}, err
	}
	previous := slices.Clone(m.vms[index].Disks)
	m.vms[index].Disks = append(m.vms[index].Disks, disk)
	if err := m.store.Save(m.vms); err != nil {
		m.vms[index].Disks = previous
		_ = removeIfExists(m.vmDiskPath(id, disk))
		return virtualMachine{}, err
	}
	return cloneVirtualMachine(m.vms[index]), nil
}

func (m *vmManager) RemoveDisk(id, diskID string) (virtualMachine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.indexByID(id)
	if index == -1 {
		return virtualMachine{}, errVMNotFound
	}
	if vmIsActive(m.vms[index]) {
		return virtualMachine{}, errDeviceRunningVM
	}
	diskIndex := slices.IndexFunc(m.vms[index].Disks, func(disk vmDisk) bool { return disk.ID == diskID })
	if diskIndex == -1 {
		return virtualMachine{}, &fieldError{field: "disk_id", message: "disk was not found"}
	}
	disk := m.vms[index].Disks[diskIndex]
	path := m.vmDiskPath(id, disk)
	stagedPath := path + ".removing"
	if err := os.Rename(path, stagedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return virtualMachine{}, fmt.Errorf("stage disk removal: %w", err)
	}
	previous := slices.Clone(m.vms[index].Disks)
	m.vms[index].Disks = slices.Delete(slices.Clone(m.vms[index].Disks), diskIndex, diskIndex+1)
	if err := m.store.Save(m.vms); err != nil {
		m.vms[index].Disks = previous
		_ = os.Rename(stagedPath, path)
		return virtualMachine{}, err
	}
	if err := removeIfExists(stagedPath); err != nil {
		return virtualMachine{}, fmt.Errorf("remove disk: %w", err)
	}
	return cloneVirtualMachine(m.vms[index]), nil
}

func (m *vmManager) AddCDROM(id, isoName string) (virtualMachine, error) {
	isoName = normalizeStorageName(isoName)
	if err := m.validateOptionalISO(isoName); err != nil {
		return virtualMachine{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.indexByID(id)
	if index == -1 {
		return virtualMachine{}, errVMNotFound
	}
	if vmIsActive(m.vms[index]) {
		return virtualMachine{}, errDeviceRunningVM
	}
	cdromID, err := randomID()
	if err != nil {
		return virtualMachine{}, fmt.Errorf("generate CD-ROM ID: %w", err)
	}
	previous := slices.Clone(m.vms[index].CDROMs)
	m.vms[index].CDROMs = append(m.vms[index].CDROMs, vmCDROM{ID: cdromID, ISOName: isoName})
	if err := m.store.Save(m.vms); err != nil {
		m.vms[index].CDROMs = previous
		return virtualMachine{}, err
	}
	return cloneVirtualMachine(m.vms[index]), nil
}

func (m *vmManager) UpdateCDROM(id, cdromID, isoName string) (virtualMachine, error) {
	isoName = normalizeStorageName(isoName)
	if err := m.validateOptionalISO(isoName); err != nil {
		return virtualMachine{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.indexByID(id)
	if index == -1 {
		return virtualMachine{}, errVMNotFound
	}
	cdromIndex := slices.IndexFunc(m.vms[index].CDROMs, func(cdrom vmCDROM) bool { return cdrom.ID == cdromID })
	if cdromIndex == -1 {
		return virtualMachine{}, &fieldError{field: "cdrom_id", message: "CD-ROM device was not found"}
	}
	previousISO := m.vms[index].CDROMs[cdromIndex].ISOName
	if previousISO == isoName {
		return cloneVirtualMachine(m.vms[index]), nil
	}
	if vmIsActive(m.vms[index]) {
		path := ""
		if isoName != "" {
			path = m.isoPath(isoName)
		}
		if err := m.hypervisor.ChangeCDROMMedia(m.vms[index], cdromID, path); err != nil {
			return virtualMachine{}, fmt.Errorf("change live CD-ROM media: %w", err)
		}
	}
	m.vms[index].CDROMs = slices.Clone(m.vms[index].CDROMs)
	m.vms[index].CDROMs[cdromIndex].ISOName = isoName
	if err := m.store.Save(m.vms); err != nil {
		m.vms[index].CDROMs[cdromIndex].ISOName = previousISO
		if vmIsActive(m.vms[index]) {
			previousPath := ""
			if previousISO != "" {
				previousPath = m.isoPath(previousISO)
			}
			_ = m.hypervisor.ChangeCDROMMedia(m.vms[index], cdromID, previousPath)
		}
		return virtualMachine{}, err
	}
	return cloneVirtualMachine(m.vms[index]), nil
}

func (m *vmManager) RemoveCDROM(id, cdromID string) (virtualMachine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.indexByID(id)
	if index == -1 {
		return virtualMachine{}, errVMNotFound
	}
	if vmIsActive(m.vms[index]) {
		return virtualMachine{}, errDeviceRunningVM
	}
	cdromIndex := slices.IndexFunc(m.vms[index].CDROMs, func(cdrom vmCDROM) bool { return cdrom.ID == cdromID })
	if cdromIndex == -1 {
		return virtualMachine{}, &fieldError{field: "cdrom_id", message: "CD-ROM device was not found"}
	}
	previous := slices.Clone(m.vms[index].CDROMs)
	m.vms[index].CDROMs = slices.Delete(slices.Clone(m.vms[index].CDROMs), cdromIndex, cdromIndex+1)
	if err := m.store.Save(m.vms); err != nil {
		m.vms[index].CDROMs = previous
		return virtualMachine{}, err
	}
	return cloneVirtualMachine(m.vms[index]), nil
}

func (m *vmManager) ConfigureIPMI(id string, config vmIPMIConfig) (virtualMachine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.indexByID(id)
	if index == -1 {
		return virtualMachine{}, errVMNotFound
	}
	previous := m.vms[index].IPMI
	if !config.Enabled {
		if m.ipmi != nil {
			m.ipmi.Disable(m.vms[index])
		}
		m.vms[index].IPMI = vmIPMIConfig{}
	} else {
		config.BridgeName = strings.TrimSpace(config.BridgeName)
		config.Address = strings.TrimSpace(config.Address)
		config.Username = strings.TrimSpace(config.Username)
		if config.Password == "" {
			config.Password = previous.Password
		}
		if err := validateIPMIConfig(config); err != nil {
			return virtualMachine{}, err
		}
		candidate := m.vms[index]
		candidate.IPMI = config
		if m.ipmi == nil {
			return virtualMachine{}, errors.New("IPMI service is unavailable")
		}
		if err := m.ipmi.Configure(candidate); err != nil {
			return virtualMachine{}, err
		}
		m.vms[index].IPMI = candidate.IPMI
	}
	if err := m.store.Save(m.vms); err != nil {
		m.vms[index].IPMI = previous
		if m.ipmi != nil {
			if previous.Enabled {
				_ = m.ipmi.Configure(m.vms[index])
			} else {
				m.ipmi.Disable(m.vms[index])
			}
		}
		return virtualMachine{}, err
	}
	return cloneVirtualMachine(m.vms[index]), nil
}

func validateIPMIConfig(config vmIPMIConfig) error {
	if err := validateInterfaceName(config.BridgeName); err != nil {
		return &fieldError{field: "bridge_name", message: err.Error()}
	}
	address, err := netip.ParseAddr(config.Address)
	if err != nil || !address.Is4() || address.IsUnspecified() || address.IsMulticast() {
		return &fieldError{field: "address", message: "must be a usable IPv4 address"}
	}
	if config.Username == "" || len(config.Username) > 16 {
		return &fieldError{field: "username", message: "must be between 1 and 16 characters"}
	}
	if config.Password == "" || len(config.Password) > 20 {
		return &fieldError{field: "password", message: "must be between 1 and 20 bytes"}
	}
	return nil
}

func (m *vmManager) SetIPMIBootFlags(id string, device uint8, persistent bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.indexByID(id)
	if index == -1 {
		return errVMNotFound
	}
	m.vms[index].IPMI.BootDevice = device
	m.vms[index].IPMI.BootPersistent = persistent
	return m.store.Save(m.vms)
}

func (m *vmManager) Reset(id string) error {
	_, err := m.Reboot(id)
	return err
}

func (m *vmManager) Reboot(id string) (virtualMachine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reconcileLocked(); err != nil {
		return virtualMachine{}, err
	}
	index := m.indexByID(id)
	if index == -1 {
		return virtualMachine{}, errVMNotFound
	}
	if m.vms[index].Status != vmStatusRunning {
		return virtualMachine{}, errVMNotRunning
	}
	if err := m.hypervisor.Reset(m.vms[index]); err != nil {
		return virtualMachine{}, fmt.Errorf("reboot virtual machine: %w", err)
	}
	return cloneVirtualMachine(m.vms[index]), nil
}

func (m *vmManager) ListISOs() ([]isoImage, error) {
	entries, err := m.listISOStorageImages(".iso")
	if err != nil {
		return nil, err
	}
	images := make([]isoImage, 0, len(entries))
	for _, entry := range entries {
		images = append(images, isoImage{Name: entry.Name, SizeBytes: entry.SizeBytes})
	}
	return images, nil
}

func (m *vmManager) ListDiskImages() ([]diskImage, error) {
	return m.listISOStorageImages(".img", ".qcow2")
}

func (m *vmManager) listISOStorageImages(extensions ...string) ([]diskImage, error) {
	allowed := make(map[string]struct{}, len(extensions))
	for _, extension := range extensions {
		allowed[strings.ToLower(extension)] = struct{}{}
	}
	images := make([]diskImage, 0)
	err := filepath.WalkDir(m.settings.ISODir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if _, ok := allowed[strings.ToLower(filepath.Ext(entry.Name()))]; !ok {
			return nil
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		relativePath, err := filepath.Rel(m.settings.ISODir, path)
		if err != nil {
			return err
		}
		images = append(images, diskImage{Name: filepath.ToSlash(relativePath), SizeBytes: uint64(info.Size())})
		return nil
	})
	if err != nil {
		return nil, err
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

func (m *vmManager) processExited(id string, expectedPID int, exitErr error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.indexByID(id)
	if index == -1 {
		return
	}
	if m.vms[index].PID == 0 || (expectedPID != 0 && m.vms[index].PID != expectedPID) {
		return
	}
	wasStopping := m.vms[index].Status == vmStatusStopping
	exitedVM := m.vms[index]
	m.vms[index].PID = 0
	m.vms[index].Status = vmStatusStopped
	m.signalExitLocked(id)
	if exitErr != nil && !wasStopping {
		m.vms[index].LastError = exitErr.Error()
	}
	m.saveLocked()
	if m.networkLifecycle != nil {
		m.networkLifecycle.Release(exitedVM)
	}
}

// saveLocked persists definitions where the caller has no way to report a
// failure to a user; the error is logged instead of silently dropped.
func (m *vmManager) saveLocked() {
	if err := m.store.Save(m.vms); err != nil {
		slog.Error("persist virtual machine definitions", "error", err)
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
			m.signalExitLocked(vm.ID)
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

func (m *vmManager) validateDiskSpec(spec vmDiskSpec) error {
	if spec.SizeGiB < 1 || spec.SizeGiB > 2048 {
		return &fieldError{field: "size_gib", message: "must be between 1 and 2048"}
	}
	if spec.SourceImage != "" {
		if _, err := m.resolveISOStorageFile(spec.SourceImage, ".img", ".qcow2"); err != nil {
			return &fieldError{field: "source_image", message: err.Error()}
		}
	}
	return nil
}

func validateCloudInit(configuration vmCloudInit) error {
	if len(configuration.UserData) > maxCloudInitUserDataBytes {
		return &fieldError{field: "cloud_init_user_data", message: "must not exceed 1 MiB"}
	}
	return nil
}

func validateVMIdentityAndResources(name, description string, cpus, memoryMiB uint32) error {
	if name == "" {
		return &fieldError{field: "name", message: "a name is required"}
	}
	if len(name) > 80 {
		return &fieldError{field: "name", message: "must not exceed 80 characters"}
	}
	if err := validateDNSLabel(name); err != nil {
		return &fieldError{field: "name", message: "must be a valid DNS hostname label: " + err.Error()}
	}
	if len(description) > 4096 {
		return &fieldError{field: "description", message: "must not exceed 4096 characters"}
	}
	if cpus < 1 || cpus > 64 {
		return &fieldError{field: "cpus", message: "must be between 1 and 64"}
	}
	if memoryMiB < 256 || memoryMiB > 1_048_576 {
		return &fieldError{field: "memory_mib", message: "must be between 256 and 1048576"}
	}
	return nil
}

func (m *vmManager) validateOptionalISO(isoName string) error {
	if isoName == "" {
		return nil
	}
	if _, err := m.resolveISOStorageFile(isoName, ".iso"); err != nil {
		return &fieldError{field: "iso_name", message: err.Error()}
	}
	return nil
}

// normalizeStorageName cleans a relative path inside ISO storage.
func normalizeStorageName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(filepath.FromSlash(name)))
}

func (m *vmManager) resolveISOStorageFile(name string, extensions ...string) (string, error) {
	cleanName := filepath.Clean(filepath.FromSlash(strings.TrimSpace(name)))
	if cleanName == "." || filepath.IsAbs(cleanName) || cleanName == ".." || strings.HasPrefix(cleanName, ".."+string(filepath.Separator)) {
		return "", errors.New("must name a file inside the configured ISO directory")
	}
	allowed := false
	for _, extension := range extensions {
		if strings.EqualFold(filepath.Ext(cleanName), extension) {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", fmt.Errorf("must use one of these file extensions: %s", strings.Join(extensions, ", "))
	}
	root, err := filepath.EvalSymlinks(m.settings.ISODir)
	if err != nil {
		return "", fmt.Errorf("resolve ISO storage: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, cleanName))
	if err != nil {
		return "", errors.New("the selected file does not exist")
	}
	relativePath, err := filepath.Rel(root, resolved)
	if err != nil || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
		return "", errors.New("must name a file inside the configured ISO directory")
	}
	if !regularFile(resolved) {
		return "", errors.New("the selected file is not a regular file")
	}
	return resolved, nil
}

func vmIsActive(vm virtualMachine) bool {
	return vm.Status == vmStatusRunning || vm.Status == vmStatusStopping
}

// bridges lists the distinct host bridges the VM's interfaces attach to.
func (vm virtualMachine) bridges() []string {
	var result []string
	for _, nic := range vm.NICs {
		if nic.Mode == networkModeBridge && nic.BridgeName != "" && !slices.Contains(result, nic.BridgeName) {
			result = append(result, nic.BridgeName)
		}
	}
	return result
}

func (vm virtualMachine) usesBridge(bridge string) bool {
	return slices.Contains(vm.bridges(), bridge)
}

func (vm virtualMachine) hasMACAddress(macAddress string) bool {
	for _, nic := range vm.NICs {
		if strings.EqualFold(nic.MACAddress, macAddress) {
			return true
		}
	}
	return false
}

func (m *vmManager) indexByID(id string) int {
	for index := range m.vms {
		if m.vms[index].ID == id {
			return index
		}
	}
	return -1
}

func (m *vmManager) vmDiskPath(vmID string, disk vmDisk) string {
	return m.settings.vmDiskPath(vmID, disk.ID)
}

func (m *vmManager) isoPath(name string) string {
	return m.settings.isoPath(name)
}

func (m *vmManager) qmpSocketPath(id string) string {
	return m.settings.qmpSocketPath(id)
}

func (m *vmManager) vncSocketPath(id string) string {
	return m.settings.vncSocketPath(id)
}

func (m *vmManager) cloudInitISOPath(id string) string {
	return m.settings.cloudInitISOPath(id)
}

func (m *vmManager) uefiVarsPath(id string) string {
	return m.settings.uefiVarsPath(id)
}

func cloudInitOptions(vm virtualMachine) cloudInitISOOptions {
	return cloudInitISOOptions{InstanceID: vm.ID, Hostname: vm.Name, UserData: vm.CloudInit.UserData}
}

func (m *vmManager) stageCloudInitISO(ctx context.Context, vm virtualMachine) (commit func(), rollback func(), err error) {
	if err := os.MkdirAll(m.settings.vmStateDir(vm.ID), 0o750); err != nil {
		return nil, nil, fmt.Errorf("create VM state directory: %w", err)
	}
	path := m.cloudInitISOPath(vm.ID)
	stagedPath := path + ".new"
	backupPath := path + ".previous"
	for _, stalePath := range []string{stagedPath, backupPath} {
		if err := removeIfExists(stalePath); err != nil {
			return nil, nil, err
		}
	}
	if vm.CloudInit.Enabled {
		if err := m.buildCloudInitISO(ctx, stagedPath, cloudInitOptions(vm)); err != nil {
			return nil, nil, err
		}
	}
	hadPrevious := false
	if _, statErr := os.Stat(path); statErr == nil {
		if err := os.Rename(path, backupPath); err != nil {
			_ = removeIfExists(stagedPath)
			return nil, nil, err
		}
		hadPrevious = true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		_ = removeIfExists(stagedPath)
		return nil, nil, statErr
	}
	if vm.CloudInit.Enabled {
		if err := os.Rename(stagedPath, path); err != nil {
			if hadPrevious {
				_ = os.Rename(backupPath, path)
			}
			return nil, nil, err
		}
	}
	commit = func() { _ = removeIfExists(backupPath) }
	rollback = func() {
		_ = removeIfExists(path)
		if hadPrevious {
			_ = os.Rename(backupPath, path)
		}
		_ = removeIfExists(stagedPath)
	}
	return commit, rollback, nil
}

func randomID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// nicMACAddress derives a stable locally administered QEMU-prefixed MAC
// address from the VM and interface identities.
func nicMACAddress(vmID, nicID string) string {
	digest := sha256.Sum256([]byte(vmID + "/" + nicID))
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

func copyVMFile(ctx context.Context, sourcePath, destinationPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(destinationPath), ".app-runner-clone-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	// Keeping both operands as *os.File lets Go use copy_file_range where available,
	// preserving sparse QCOW2 regions without creating a QCOW2 backing-file link.
	if _, err := io.Copy(temporary, source); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := ctx.Err(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Link(temporaryPath, destinationPath)
}
