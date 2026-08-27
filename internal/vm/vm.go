// Package vm defines virtual machines and the manager that owns their
// lifecycle, definitions, and files. It drives a Hypervisor and consults
// host networking and BMC services only through interfaces, so it has no
// dependency on the packages that implement them.
package vm

import (
	"context"
	"crypto/sha256"
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

	"github.com/roflware/app-runner/internal/cloudinit"
	"github.com/roflware/app-runner/internal/config"
	"github.com/roflware/app-runner/internal/fsutil"
	"github.com/roflware/app-runner/internal/validate"
)

type Status string

type Firmware string

const (
	StatusStopped  Status = "stopped"
	StatusRunning  Status = "running"
	StatusStopping Status = "stopping"
	StatusError    Status = "error"

	FirmwareBIOS Firmware = "bios"
	FirmwareUEFI Firmware = "uefi"
)

type NetworkMode string

const (
	NetworkModeNAT    NetworkMode = "nat"
	NetworkModeBridge NetworkMode = "bridge"
)

var (
	ErrNotFound          = errors.New("virtual machine not found")
	ErrAlreadyRunning    = errors.New("virtual machine is already running")
	ErrNotRunning        = errors.New("virtual machine is not running")
	ErrNameExists        = errors.New("a virtual machine with that name already exists")
	ErrBridgeUnavailable = errors.New("bridge networking is unavailable")
	ErrHostUnavailable   = errors.New("required host virtualisation support is unavailable")
	ErrDeleteRunning     = errors.New("stop the virtual machine before deleting it")
	ErrDeviceRunning     = errors.New("stop the virtual machine before changing its device configuration")
	ErrCloneRunning      = errors.New("stop the virtual machine before cloning it")
	ErrBusy              = errors.New("the virtual machine is busy with another operation")
)

// VirtualMachine is the persistent definition of a QEMU virtual machine. A VM
// may have any number of disks, CD-ROM drives, and network interfaces,
// including none; nothing about its devices is implied.
type VirtualMachine struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	CPUs        uint32     `json:"cpus"`
	MemoryMiB   uint32     `json:"memory_mib"`
	Firmware    Firmware   `json:"firmware"`
	Status      Status     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	LastError   string     `json:"last_error,omitempty"`
	PID         int        `json:"pid,omitempty"`
	Disks       []Disk     `json:"disks"`
	CDROMs      []CDROM    `json:"cdroms"`
	NICs        []NIC      `json:"nics"`
	CloudInit   CloudInit  `json:"cloud_init,omitempty"`
	IPMI        IPMIConfig `json:"ipmi,omitempty"`
	IPMIRunning bool       `json:"-"`
	IPMIError   string     `json:"-"`
}

// Disk is a qcow2 disk image. Disks are attached in list order, so the
// first disk is the one firmware boots from. SourceImage names a cloud image
// inside ISO storage that the disk is a linked overlay of.
type Disk struct {
	ID          string `json:"id"`
	SizeGiB     uint32 `json:"size_gib"`
	SourceImage string `json:"source_image,omitempty"`
}

type CDROM struct {
	ID      string `json:"id"`
	ISOName string `json:"iso_name,omitempty"`
}

type NIC struct {
	ID         string      `json:"id"`
	Mode       NetworkMode `json:"mode"`
	BridgeName string      `json:"bridge_name,omitempty"`
	MACAddress string      `json:"mac_address"`
	// Addresses are the IPv4 leases currently held from managed DHCP; they
	// are looked up when a VM is read rather than stored.
	Addresses []string `json:"-"`
}

// sameDefinition compares the stored identity of two interfaces, ignoring
// runtime address annotations.
func (nic NIC) sameDefinition(other NIC) bool {
	return nic.ID == other.ID && nic.Mode == other.Mode && nic.BridgeName == other.BridgeName &&
		strings.EqualFold(nic.MACAddress, other.MACAddress)
}

// Update carries the settings an update may change; nil fields are left
// as they are.
type Update struct {
	Name        *string
	Description *string
	CPUs        *uint32
	MemoryMiB   *uint32
}

type CloudInit struct {
	Enabled  bool   `json:"enabled,omitempty"`
	UserData string `json:"user_data,omitempty"`
}

type IPMIConfig struct {
	Enabled        bool   `json:"enabled,omitempty"`
	BridgeName     string `json:"bridge_name,omitempty"`
	Address        string `json:"address,omitempty"`
	Username       string `json:"username,omitempty"`
	Password       string `json:"password,omitempty"`
	BootDevice     uint8  `json:"boot_device,omitempty"`
	BootPersistent bool   `json:"boot_persistent,omitempty"`
}

// DiskSpec, CDROMSpec, and NICSpec describe devices to add; identities
// are assigned by the manager.
type DiskSpec struct {
	SizeGiB     uint32
	SourceImage string
}

type CDROMSpec struct {
	ISOName string
}

type NICSpec struct {
	Mode       NetworkMode
	BridgeName string
	MACAddress string
}

// isoBuilder produces a NoCloud ISO; tests substitute it.
type isoBuilder func(context.Context, string, cloudinit.ISOOptions) error

type IPMIController interface {
	Configure(VirtualMachine) error
	Disable(VirtualMachine)
	Status(string) (bool, string)
}

type CreateOptions struct {
	Name        string
	Description string
	CPUs        uint32
	MemoryMiB   uint32
	Firmware    Firmware
	Disks       []DiskSpec
	CDROMs      []CDROMSpec
	NICs        []NICSpec
	CloudInit   CloudInit
}

type ISOImage struct {
	Name      string
	SizeBytes uint64
}

type DiskImage struct {
	Name      string
	SizeBytes uint64
}

type HostCapabilities struct {
	QEMUAvailable bool
	KVMAvailable  bool
}

type Hypervisor interface {
	CreateDisk(context.Context, string, uint32) error
	CreateLinkedDisk(context.Context, string, string, uint32) error
	Start(VirtualMachine, func(int, error)) (int, error)
	IsRunning(VirtualMachine) bool
	GracefulStop(VirtualMachine) error
	ForceStop(VirtualMachine) error
	Reset(VirtualMachine) error
	ChangeCDROMMedia(VirtualMachine, string, string) error
}

type Store interface {
	Load() ([]VirtualMachine, error)
	Save([]VirtualMachine) error
}

type NetworkLifecycle interface {
	Prepare(VirtualMachine) error
	Release(VirtualMachine)
}

// ManagerOptions wires the VM manager to the host facilities it drives.
// Addresses, when set, resolves the managed DHCP leases held by a MAC
// address on a bridge so read VMs report their guest addresses.
type ManagerOptions struct {
	Settings         config.Config
	Store            Store
	Hypervisor       Hypervisor
	Capabilities     func() HostCapabilities
	BridgeCapability func(string) (bool, string)
	Addresses        func(bridge, macAddress string) []string
	NetworkLifecycle NetworkLifecycle
	// Now and NewID default to the wall clock and random identities.
	Now   func() time.Time
	NewID func() (string, error)
}

type Manager struct {
	mu                sync.Mutex
	settings          config.Config
	store             Store
	hypervisor        Hypervisor
	capabilities      func() HostCapabilities
	bridgeCapability  func(string) (bool, string)
	addresses         func(bridge, macAddress string) []string
	networkLifecycle  NetworkLifecycle
	ipmi              IPMIController
	now               func() time.Time
	newID             func() (string, error)
	buildCloudInitISO isoBuilder
	copyFile          func(context.Context, string, string) error
	vms               []VirtualMachine
	// creating holds definitions whose files are still being produced with
	// the lock released; they reserve names and MAC addresses but are not
	// yet listed.
	creating map[string]VirtualMachine
	// busy records VMs whose disks are being copied or created with the lock
	// released, keyed by ID with a short description of the operation.
	busy map[string]string
	// exits holds, per running VM, a channel closed once its process exit
	// has been observed and its runtime files cleaned up.
	exits map[string]chan struct{}
	// stopTimers escalate graceful shutdowns that the guest ignores.
	stopTimers       map[string]*time.Timer
	forceStopTimeout time.Duration
}

// defaultForceStopTimeout bounds how long a forced stop waits for the killed
// process to be reaped before reporting the VM stopped anyway.
const defaultForceStopTimeout = 10 * time.Second

func NewManager(options ManagerOptions) (*Manager, error) {
	vms, err := options.Store.Load()
	if err != nil {
		return nil, err
	}
	settings := options.Settings
	manager := &Manager{
		settings:         settings,
		store:            options.Store,
		hypervisor:       options.Hypervisor,
		capabilities:     options.Capabilities,
		bridgeCapability: options.BridgeCapability,
		addresses:        options.Addresses,
		networkLifecycle: options.NetworkLifecycle,
		now:              time.Now,
		newID:            fsutil.RandomID,
		exits:            make(map[string]chan struct{}),
		stopTimers:       make(map[string]*time.Timer),
		creating:         make(map[string]VirtualMachine),
		busy:             make(map[string]string),
		forceStopTimeout: defaultForceStopTimeout,
		buildCloudInitISO: func(ctx context.Context, destination string, options cloudinit.ISOOptions) error {
			return cloudinit.BuildISO(ctx, settings.XorrisoBinary, destination, options)
		},
		copyFile: copyFile,
		vms:      vms,
	}
	if manager.bridgeCapability == nil {
		manager.bridgeCapability = func(string) (bool, string) { return false, "bridge networking is not configured" }
	}
	if options.Now != nil {
		manager.now = options.Now
	}
	if options.NewID != nil {
		manager.newID = options.NewID
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

// SetIPMIController attaches the virtual BMC service, which is constructed
// after the manager because it drives the manager.
func (m *Manager) SetIPMIController(controller IPMIController) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ipmi = controller
}

func (m *Manager) validateStoredDefinitions() error {
	for index := range m.vms {
		vm := &m.vms[index]
		if vm.ID == "" {
			return fmt.Errorf("stored virtual machine %q has no ID", vm.Name)
		}
		if vm.Firmware == "" {
			vm.Firmware = FirmwareBIOS
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

func (m *Manager) List() ([]VirtualMachine, error) {
	m.mu.Lock()
	if err := m.reconcileLocked(); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	result := make([]VirtualMachine, 0, len(m.vms))
	for _, vm := range m.vms {
		result = append(result, cloneDefinition(vm))
	}
	m.mu.Unlock()
	for index := range result {
		m.annotate(&result[index])
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].CreatedAt.Before(result[right].CreatedAt)
	})
	return result, nil
}

func (m *Manager) Get(id string) (VirtualMachine, error) {
	m.mu.Lock()
	if err := m.reconcileLocked(); err != nil {
		m.mu.Unlock()
		return VirtualMachine{}, err
	}
	index := m.indexByID(id)
	if index == -1 {
		m.mu.Unlock()
		return VirtualMachine{}, ErrNotFound
	}
	result := cloneDefinition(m.vms[index])
	m.mu.Unlock()
	m.annotate(&result)
	return result, nil
}

// annotated returns a copy of a stored definition with runtime information
// attached; it is what every read or mutation hands back to callers.
func (m *Manager) annotated(vm VirtualMachine) VirtualMachine {
	result := cloneDefinition(vm)
	m.annotate(&result)
	return result
}

// annotate adds runtime information that is not part of the stored
// definition: BMC status and leased guest addresses. The IPMI and DHCP
// managers are always acquired after the VM manager lock, so this is safe
// with or without it held.
func (m *Manager) annotate(vm *VirtualMachine) {
	if m.ipmi != nil {
		vm.IPMIRunning, vm.IPMIError = m.ipmi.Status(vm.ID)
	}
	if m.addresses == nil {
		return
	}
	for index := range vm.NICs {
		nic := &vm.NICs[index]
		if nic.Mode == NetworkModeBridge && nic.BridgeName != "" {
			nic.Addresses = m.addresses(nic.BridgeName, nic.MACAddress)
		}
	}
}

// cloneDefinition copies a definition so callers cannot alias the
// manager's device slices.
func cloneDefinition(vm VirtualMachine) VirtualMachine {
	vm.Disks = slices.Clone(vm.Disks)
	vm.CDROMs = slices.Clone(vm.CDROMs)
	vm.NICs = slices.Clone(vm.NICs)
	return vm
}

func (m *Manager) Create(ctx context.Context, options CreateOptions) (VirtualMachine, error) {
	options.Name = strings.TrimSpace(options.Name)
	options.Description = strings.TrimSpace(options.Description)
	if err := validateIdentityAndResources(options.Name, options.Description, options.CPUs, options.MemoryMiB); err != nil {
		return VirtualMachine{}, err
	}
	for index := range options.Disks {
		options.Disks[index].SourceImage = normalizeStorageName(options.Disks[index].SourceImage)
		if err := m.validateDiskSpec(options.Disks[index]); err != nil {
			return VirtualMachine{}, err
		}
	}
	for index := range options.CDROMs {
		options.CDROMs[index].ISOName = normalizeStorageName(options.CDROMs[index].ISOName)
		if err := m.validateOptionalISO(options.CDROMs[index].ISOName); err != nil {
			return VirtualMachine{}, err
		}
	}
	for index := range options.NICs {
		normalized, err := normalizeNICSpec(options.NICs[index])
		if err != nil {
			return VirtualMachine{}, err
		}
		options.NICs[index] = normalized
	}
	if err := validateCloudInit(options.CloudInit); err != nil {
		return VirtualMachine{}, err
	}
	if !options.CloudInit.Enabled {
		options.CloudInit = CloudInit{}
	}
	firmware := options.Firmware
	if firmware == "" {
		firmware = FirmwareBIOS
		for _, disk := range options.Disks {
			if disk.SourceImage != "" {
				firmware = FirmwareUEFI
			}
		}
	}
	if firmware != FirmwareBIOS && firmware != FirmwareUEFI {
		return VirtualMachine{}, validate.Field("firmware", "must be bios or uefi")
	}
	if firmware == FirmwareUEFI && (!config.RegularFile(m.settings.UEFICodePath) || !config.RegularFile(m.settings.UEFIVarsPath)) {
		return VirtualMachine{}, validate.Field("firmware", "UEFI firmware is unavailable; configure uefi_code_path and uefi_vars_path")
	}

	// Reserve the identity under the lock, then produce files without it so
	// long-running qemu-img work does not stall every other request.
	m.mu.Lock()
	vm, err := m.reserveVMLocked(options, firmware)
	m.mu.Unlock()
	if err != nil {
		return VirtualMachine{}, err
	}

	created := make([]string, 0, len(vm.Disks))
	cleanup := func() {
		for _, path := range created {
			_ = fsutil.RemoveIfExists(path)
		}
		_ = os.RemoveAll(m.settings.VMStateDir(vm.ID))
	}
	produce := func() error {
		for _, disk := range vm.Disks {
			if err := m.createDiskFile(ctx, vm.ID, disk); err != nil {
				return err
			}
			created = append(created, m.diskPath(vm.ID, disk))
		}
		if err := os.MkdirAll(m.settings.VMStateDir(vm.ID), 0o750); err != nil {
			return fmt.Errorf("create VM state directory: %w", err)
		}
		if vm.Firmware == FirmwareUEFI {
			if err := m.copyFile(ctx, m.settings.UEFIVarsPath, m.uefiVars(vm.ID)); err != nil {
				return fmt.Errorf("create UEFI variable store: %w", err)
			}
		}
		if vm.CloudInit.Enabled {
			return m.buildCloudInitISO(ctx, m.cloudInitISO(vm.ID), isoOptions(vm))
		}
		return nil
	}
	err = produce()

	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.creating, vm.ID)
	if err != nil {
		cleanup()
		return VirtualMachine{}, err
	}
	m.vms = append(m.vms, vm)
	if err := m.store.Save(m.vms); err != nil {
		m.vms = m.vms[:len(m.vms)-1]
		cleanup()
		return VirtualMachine{}, err
	}
	return m.annotated(vm), nil
}

// reserveVMLocked allocates a definition for validated options and records it
// as in progress so its name and MAC addresses cannot be claimed twice.
func (m *Manager) reserveVMLocked(options CreateOptions, firmware Firmware) (VirtualMachine, error) {
	if m.nameInUseLocked(options.Name, "") {
		return VirtualMachine{}, ErrNameExists
	}
	id, err := m.newID()
	if err != nil {
		return VirtualMachine{}, fmt.Errorf("generate virtual machine ID: %w", err)
	}
	vm := VirtualMachine{
		ID:          id,
		Name:        options.Name,
		Description: options.Description,
		CPUs:        options.CPUs,
		MemoryMiB:   options.MemoryMiB,
		Firmware:    firmware,
		Status:      StatusStopped,
		CreatedAt:   m.now().UTC(),
		Disks:       make([]Disk, 0, len(options.Disks)),
		CDROMs:      make([]CDROM, 0, len(options.CDROMs)),
		NICs:        make([]NIC, 0, len(options.NICs)),
		CloudInit:   options.CloudInit,
	}
	for _, spec := range options.Disks {
		diskID, err := fsutil.RandomID()
		if err != nil {
			return VirtualMachine{}, fmt.Errorf("generate disk ID: %w", err)
		}
		vm.Disks = append(vm.Disks, Disk{ID: diskID, SizeGiB: spec.SizeGiB, SourceImage: spec.SourceImage})
	}
	for _, spec := range options.CDROMs {
		cdromID, err := fsutil.RandomID()
		if err != nil {
			return VirtualMachine{}, fmt.Errorf("generate CD-ROM ID: %w", err)
		}
		vm.CDROMs = append(vm.CDROMs, CDROM{ID: cdromID, ISOName: spec.ISOName})
	}
	for _, spec := range options.NICs {
		nic, err := m.newNICLocked(vm, spec)
		if err != nil {
			return VirtualMachine{}, err
		}
		vm.NICs = append(vm.NICs, nic)
	}
	m.creating[vm.ID] = vm
	return vm, nil
}

func (m *Manager) nameInUseLocked(name, excludeID string) bool {
	for _, existing := range m.vms {
		if existing.ID != excludeID && strings.EqualFold(existing.Name, name) {
			return true
		}
	}
	for _, pending := range m.creating {
		if strings.EqualFold(pending.Name, name) {
			return true
		}
	}
	return false
}

// requireIdleLocked rejects changes to a VM whose files are being produced
// or copied outside the lock.
func (m *Manager) requireIdleLocked(id string) error {
	if reason, busy := m.busy[id]; busy {
		return fmt.Errorf("%w: %s", ErrBusy, reason)
	}
	return nil
}

func (m *Manager) createDiskFile(ctx context.Context, vmID string, disk Disk) error {
	path := m.diskPath(vmID, disk)
	if disk.SourceImage == "" {
		if err := m.hypervisor.CreateDisk(ctx, path, disk.SizeGiB); err != nil {
			return fmt.Errorf("create disk: %w", err)
		}
		return nil
	}
	sourcePath, err := m.resolveISOStorageFile(disk.SourceImage, ".img", ".qcow2")
	if err != nil {
		return validate.Field("source_image", err.Error())
	}
	if err := m.hypervisor.CreateLinkedDisk(ctx, path, sourcePath, disk.SizeGiB); err != nil {
		return fmt.Errorf("create linked disk: %w", err)
	}
	return nil
}

func (m *Manager) Clone(ctx context.Context, id, name string) (VirtualMachine, error) {
	name = strings.TrimSpace(name)
	m.mu.Lock()
	if err := m.reconcileLocked(); err != nil {
		m.mu.Unlock()
		return VirtualMachine{}, err
	}
	index := m.indexByID(id)
	if index == -1 {
		m.mu.Unlock()
		return VirtualMachine{}, ErrNotFound
	}
	source := cloneDefinition(m.vms[index])
	if source.Status != StatusStopped {
		m.mu.Unlock()
		return VirtualMachine{}, ErrCloneRunning
	}
	if err := m.requireIdleLocked(id); err != nil {
		m.mu.Unlock()
		return VirtualMachine{}, err
	}
	if err := validateIdentityAndResources(name, source.Description, source.CPUs, source.MemoryMiB); err != nil {
		m.mu.Unlock()
		return VirtualMachine{}, err
	}
	clone, err := m.reserveCloneLocked(source, name)
	if err != nil {
		m.mu.Unlock()
		return VirtualMachine{}, err
	}
	// The source stays readable but cannot be started, deleted, or changed
	// while its disks are copied with the lock released.
	m.busy[source.ID] = "cloning"
	m.mu.Unlock()

	createdPaths := make([]string, 0, len(clone.Disks))
	removeCreated := func() {
		for _, path := range createdPaths {
			_ = fsutil.RemoveIfExists(path)
		}
		_ = os.RemoveAll(m.settings.VMStateDir(clone.ID))
	}
	copyErr := func() error {
		if err := os.MkdirAll(m.settings.VMStateDir(clone.ID), 0o750); err != nil {
			return fmt.Errorf("create VM state directory: %w", err)
		}
		for diskIndex, sourceDisk := range source.Disks {
			destinationPath := m.diskPath(clone.ID, clone.Disks[diskIndex])
			if err := m.copyFile(ctx, m.diskPath(source.ID, sourceDisk), destinationPath); err != nil {
				return fmt.Errorf("copy disk %s: %w", sourceDisk.ID, err)
			}
			createdPaths = append(createdPaths, destinationPath)
		}
		if clone.CloudInit.Enabled {
			if err := m.buildCloudInitISO(ctx, m.cloudInitISO(clone.ID), isoOptions(clone)); err != nil {
				return err
			}
		}
		if clone.Firmware == FirmwareUEFI {
			if err := m.copyFile(ctx, m.uefiVars(source.ID), m.uefiVars(clone.ID)); err != nil {
				return fmt.Errorf("clone UEFI variable store: %w", err)
			}
		}
		return nil
	}()

	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.busy, source.ID)
	delete(m.creating, clone.ID)
	if copyErr != nil {
		removeCreated()
		return VirtualMachine{}, copyErr
	}
	m.vms = append(m.vms, clone)
	if err := m.store.Save(m.vms); err != nil {
		m.vms = m.vms[:len(m.vms)-1]
		removeCreated()
		return VirtualMachine{}, err
	}
	return m.annotated(clone), nil
}

// reserveCloneLocked allocates the clone's identity: new device IDs and MAC
// addresses, the same configuration, and no IPMI endpoint.
func (m *Manager) reserveCloneLocked(source VirtualMachine, name string) (VirtualMachine, error) {
	if m.nameInUseLocked(name, "") {
		return VirtualMachine{}, ErrNameExists
	}
	cloneID, err := m.newID()
	if err != nil {
		return VirtualMachine{}, fmt.Errorf("generate cloned virtual machine ID: %w", err)
	}
	if m.indexByID(cloneID) != -1 {
		return VirtualMachine{}, errors.New("generated cloned virtual machine ID already exists")
	}
	clone := VirtualMachine{
		ID:          cloneID,
		Name:        name,
		Description: source.Description,
		CPUs:        source.CPUs,
		MemoryMiB:   source.MemoryMiB,
		Firmware:    source.Firmware,
		Status:      StatusStopped,
		CreatedAt:   m.now().UTC(),
		Disks:       make([]Disk, 0, len(source.Disks)),
		CDROMs:      make([]CDROM, 0, len(source.CDROMs)),
		NICs:        make([]NIC, 0, len(source.NICs)),
		CloudInit:   source.CloudInit,
	}
	for _, sourceDisk := range source.Disks {
		clonedDisk := sourceDisk
		clonedDisk.ID, err = fsutil.RandomID()
		if err != nil {
			return VirtualMachine{}, fmt.Errorf("generate cloned disk ID: %w", err)
		}
		clone.Disks = append(clone.Disks, clonedDisk)
	}
	for _, sourceCDROM := range source.CDROMs {
		cdromID, err := fsutil.RandomID()
		if err != nil {
			return VirtualMachine{}, fmt.Errorf("generate cloned CD-ROM ID: %w", err)
		}
		clone.CDROMs = append(clone.CDROMs, CDROM{ID: cdromID, ISOName: sourceCDROM.ISOName})
	}
	for _, sourceNIC := range source.NICs {
		nic, err := m.newNICLocked(clone, NICSpec{Mode: sourceNIC.Mode, BridgeName: sourceNIC.BridgeName})
		if err != nil {
			return VirtualMachine{}, err
		}
		clone.NICs = append(clone.NICs, nic)
	}
	m.creating[clone.ID] = clone
	return clone, nil
}

func (m *Manager) Start(id string) (VirtualMachine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reconcileLocked(); err != nil {
		return VirtualMachine{}, err
	}
	index := m.indexByID(id)
	if index == -1 {
		return VirtualMachine{}, ErrNotFound
	}
	if err := m.requireIdleLocked(id); err != nil {
		return VirtualMachine{}, err
	}
	vm := m.vms[index]
	if vm.Status == StatusRunning || vm.Status == StatusStopping {
		return VirtualMachine{}, ErrAlreadyRunning
	}
	capabilities := m.capabilities()
	if !capabilities.QEMUAvailable {
		return VirtualMachine{}, fmt.Errorf("%w: QEMU is not available", ErrHostUnavailable)
	}
	if !capabilities.KVMAvailable {
		return VirtualMachine{}, fmt.Errorf("%w: KVM is not available to the current user", ErrHostUnavailable)
	}
	for _, bridge := range vm.Bridges() {
		available, warning := m.bridgeCapability(bridge)
		if !available {
			return VirtualMachine{}, fmt.Errorf("%w: %s", ErrBridgeUnavailable, warning)
		}
	}
	for _, disk := range vm.Disks {
		if _, err := os.Stat(m.diskPath(vm.ID, disk)); err != nil {
			return VirtualMachine{}, fmt.Errorf("disk %s is unavailable: %w", disk.ID, err)
		}
		if disk.SourceImage != "" {
			if _, err := m.resolveISOStorageFile(disk.SourceImage, ".img", ".qcow2"); err != nil {
				return VirtualMachine{}, fmt.Errorf("cloud image %s for disk %s is unavailable: %w", disk.SourceImage, disk.ID, err)
			}
		}
	}
	for _, cdrom := range vm.CDROMs {
		if cdrom.ISOName == "" {
			continue
		}
		if _, err := os.Stat(m.isoFile(cdrom.ISOName)); err != nil {
			return VirtualMachine{}, fmt.Errorf("CD-ROM ISO %s is unavailable: %w", cdrom.ISOName, err)
		}
	}
	if vm.CloudInit.Enabled {
		if _, err := os.Stat(m.cloudInitISO(vm.ID)); err != nil {
			return VirtualMachine{}, fmt.Errorf("cloud-init ISO is unavailable: %w", err)
		}
	}
	if vm.Firmware == FirmwareUEFI {
		for label, path := range map[string]string{
			"UEFI code image":     m.settings.UEFICodePath,
			"UEFI variable store": m.uefiVars(vm.ID),
		} {
			if !config.RegularFile(path) {
				return VirtualMachine{}, fmt.Errorf("%s is unavailable: %s", label, path)
			}
		}
	}
	if m.networkLifecycle != nil {
		if err := m.networkLifecycle.Prepare(vm); err != nil {
			return VirtualMachine{}, fmt.Errorf("%w: %v", ErrBridgeUnavailable, err)
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
		m.vms[index].Status = StatusError
		m.vms[index].LastError = err.Error()
		m.saveLocked()
		return VirtualMachine{}, fmt.Errorf("start virtual machine: %w", err)
	}
	m.vms[index].PID = pid
	m.vms[index].Status = StatusRunning
	m.vms[index].LastError = ""
	if m.vms[index].IPMI.BootDevice != 0 && !m.vms[index].IPMI.BootPersistent {
		m.vms[index].IPMI.BootDevice = 0
	}
	if err := m.store.Save(m.vms); err != nil {
		_ = m.hypervisor.ForceStop(m.vms[index])
		if m.networkLifecycle != nil {
			m.networkLifecycle.Release(vm)
		}
		return VirtualMachine{}, err
	}
	return m.annotated(m.vms[index]), nil
}

// Stop powers a VM off. Without force it asks the guest to shut down; when
// timeout is positive and the guest has not exited by then, the VM is
// forced off instead of staying in the stopping state indefinitely.
func (m *Manager) Stop(id string, force bool, timeout time.Duration) (VirtualMachine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reconcileLocked(); err != nil {
		return VirtualMachine{}, err
	}
	index := m.indexByID(id)
	if index == -1 {
		return VirtualMachine{}, ErrNotFound
	}
	if m.vms[index].Status != StatusRunning && m.vms[index].Status != StatusStopping {
		return VirtualMachine{}, ErrNotRunning
	}

	if force {
		return m.forceStopLocked(index)
	}
	if err := m.hypervisor.GracefulStop(m.vms[index]); err != nil {
		return VirtualMachine{}, err
	}
	m.vms[index].Status = StatusStopping
	if err := m.store.Save(m.vms); err != nil {
		return VirtualMachine{}, err
	}
	m.cancelStopTimerLocked(id)
	if timeout > 0 {
		pid := m.vms[index].PID
		m.stopTimers[id] = time.AfterFunc(timeout, func() { m.escalateStop(id, pid) })
	}
	return m.annotated(m.vms[index]), nil
}

// escalateStop forces a VM off after its graceful shutdown deadline passed
// without the guest exiting.
func (m *Manager) escalateStop(id string, pid int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.stopTimers, id)
	index := m.indexByID(id)
	if index == -1 || m.vms[index].Status != StatusStopping || m.vms[index].PID != pid {
		return
	}
	slog.Warn("graceful shutdown timed out; forcing the virtual machine off", "vm", m.vms[index].Name)
	if _, err := m.forceStopLocked(index); err != nil {
		slog.Error("force stop after shutdown timeout", "vm", id, "error", err)
	}
}

func (m *Manager) cancelStopTimerLocked(id string) {
	if timer := m.stopTimers[id]; timer != nil {
		timer.Stop()
		delete(m.stopTimers, id)
	}
}

// forceStopLocked kills the VM process and waits, with the manager unlocked,
// until its exit has been processed so that runtime sockets are gone and a
// following Start cannot race the dying process. The lock is held on entry
// and on return.
func (m *Manager) forceStopLocked(index int) (VirtualMachine, error) {
	id := m.vms[index].ID
	m.cancelStopTimerLocked(id)
	if err := m.hypervisor.ForceStop(m.vms[index]); err != nil {
		return VirtualMachine{}, err
	}
	m.vms[index].Status = StatusStopping
	if err := m.store.Save(m.vms); err != nil {
		return VirtualMachine{}, err
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
		return VirtualMachine{}, ErrNotFound
	}
	if m.vms[index].Status != StatusStopped {
		stoppedVM := m.vms[index]
		m.vms[index].Status = StatusStopped
		m.vms[index].PID = 0
		m.signalExitLocked(id)
		if m.networkLifecycle != nil {
			m.networkLifecycle.Release(stoppedVM)
		}
		if err := m.store.Save(m.vms); err != nil {
			return VirtualMachine{}, err
		}
	}
	return m.annotated(m.vms[index]), nil
}

// signalExitLocked wakes anything waiting for the VM's process to exit and
// drops any pending shutdown escalation.
func (m *Manager) signalExitLocked(id string) {
	m.cancelStopTimerLocked(id)
	if exited := m.exits[id]; exited != nil {
		close(exited)
		delete(m.exits, id)
	}
}

func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reconcileLocked(); err != nil {
		return err
	}
	index := m.indexByID(id)
	if index == -1 {
		return ErrNotFound
	}
	if err := m.requireIdleLocked(id); err != nil {
		return err
	}
	if m.vms[index].Status == StatusRunning || m.vms[index].Status == StatusStopping {
		return ErrDeleteRunning
	}
	vm := m.vms[index]
	if m.ipmi != nil {
		m.ipmi.Disable(vm)
	}
	for _, disk := range vm.Disks {
		if err := fsutil.RemoveIfExists(m.diskPath(vm.ID, disk)); err != nil {
			return fmt.Errorf("remove disk %s: %w", disk.ID, err)
		}
	}
	for _, path := range []string{m.qmpSocket(vm.ID), m.vncSocket(vm.ID)} {
		if err := fsutil.RemoveIfExists(path); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(m.settings.VMStateDir(vm.ID)); err != nil {
		return fmt.Errorf("remove VM state: %w", err)
	}
	m.vms = append(m.vms[:index], m.vms[index+1:]...)
	return m.store.Save(m.vms)
}

func (m *Manager) Update(id string, update Update) (VirtualMachine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reconcileLocked(); err != nil {
		return VirtualMachine{}, err
	}
	index := m.indexByID(id)
	if index == -1 {
		return VirtualMachine{}, ErrNotFound
	}
	if err := m.requireIdleLocked(id); err != nil {
		return VirtualMachine{}, err
	}
	previous := m.vms[index]
	candidate := previous
	if update.Name != nil {
		candidate.Name = strings.TrimSpace(*update.Name)
	}
	if update.Description != nil {
		candidate.Description = strings.TrimSpace(*update.Description)
	}
	if update.CPUs != nil {
		candidate.CPUs = *update.CPUs
	}
	if update.MemoryMiB != nil {
		candidate.MemoryMiB = *update.MemoryMiB
	}
	if err := validateIdentityAndResources(candidate.Name, candidate.Description, candidate.CPUs, candidate.MemoryMiB); err != nil {
		return VirtualMachine{}, err
	}
	if candidate.Name != previous.Name && m.nameInUseLocked(candidate.Name, id) {
		return VirtualMachine{}, ErrNameExists
	}
	m.vms[index] = candidate
	commitCloudInit := func() {}
	rollbackCloudInit := func() {}
	if previous.Name != candidate.Name && candidate.CloudInit.Enabled {
		var stageErr error
		commitCloudInit, rollbackCloudInit, stageErr = m.stageCloudInitISO(context.Background(), candidate)
		if stageErr != nil {
			m.vms[index] = previous
			return VirtualMachine{}, stageErr
		}
	}
	if err := m.store.Save(m.vms); err != nil {
		rollbackCloudInit()
		m.vms[index] = previous
		return VirtualMachine{}, err
	}
	commitCloudInit()
	return m.annotated(m.vms[index]), nil
}

func (m *Manager) ConfigureCloudInit(ctx context.Context, id string, configuration CloudInit) (VirtualMachine, error) {
	if err := validateCloudInit(configuration); err != nil {
		return VirtualMachine{}, err
	}
	if !configuration.Enabled {
		configuration = CloudInit{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reconcileLocked(); err != nil {
		return VirtualMachine{}, err
	}
	index := m.indexByID(id)
	if index == -1 {
		return VirtualMachine{}, ErrNotFound
	}
	if err := m.requireIdleLocked(id); err != nil {
		return VirtualMachine{}, err
	}
	previous := m.vms[index]
	if previous.CloudInit == configuration {
		return m.annotated(previous), nil
	}
	if IsActive(previous) {
		return VirtualMachine{}, ErrDeviceRunning
	}
	candidate := previous
	candidate.CloudInit = configuration
	commit, rollback, err := m.stageCloudInitISO(ctx, candidate)
	if err != nil {
		return VirtualMachine{}, err
	}
	m.vms[index] = candidate
	if err := m.store.Save(m.vms); err != nil {
		m.vms[index] = previous
		rollback()
		return VirtualMachine{}, err
	}
	commit()
	return m.annotated(candidate), nil
}

// AddNIC attaches a network interface to a stopped VM.
func (m *Manager) AddNIC(id string, spec NICSpec) (VirtualMachine, error) {
	spec, err := normalizeNICSpec(spec)
	if err != nil {
		return VirtualMachine{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reconcileLocked(); err != nil {
		return VirtualMachine{}, err
	}
	index := m.indexByID(id)
	if index == -1 {
		return VirtualMachine{}, ErrNotFound
	}
	if err := m.requireIdleLocked(id); err != nil {
		return VirtualMachine{}, err
	}
	if IsActive(m.vms[index]) {
		return VirtualMachine{}, ErrDeviceRunning
	}
	nic, err := m.newNICLocked(m.vms[index], spec)
	if err != nil {
		return VirtualMachine{}, err
	}
	previous := slices.Clone(m.vms[index].NICs)
	m.vms[index].NICs = append(m.vms[index].NICs, nic)
	if err := m.store.Save(m.vms); err != nil {
		m.vms[index].NICs = previous
		return VirtualMachine{}, err
	}
	return m.annotated(m.vms[index]), nil
}

// UpdateNIC changes a network interface. Saving an unchanged interface is
// allowed while the VM runs; any real change requires it to be stopped.
func (m *Manager) UpdateNIC(id, nicID string, spec NICSpec) (VirtualMachine, error) {
	spec, err := normalizeNICSpec(spec)
	if err != nil {
		return VirtualMachine{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reconcileLocked(); err != nil {
		return VirtualMachine{}, err
	}
	index := m.indexByID(id)
	if index == -1 {
		return VirtualMachine{}, ErrNotFound
	}
	if err := m.requireIdleLocked(id); err != nil {
		return VirtualMachine{}, err
	}
	nicIndex := slices.IndexFunc(m.vms[index].NICs, func(nic NIC) bool { return nic.ID == nicID })
	if nicIndex == -1 {
		return VirtualMachine{}, validate.Field("nic_id", "network interface was not found")
	}
	current := m.vms[index].NICs[nicIndex]
	updated := NIC{ID: current.ID, Mode: spec.Mode, BridgeName: spec.BridgeName, MACAddress: spec.MACAddress}
	if updated.MACAddress == "" {
		updated.MACAddress = current.MACAddress
	}
	if updated.sameDefinition(current) {
		return m.annotated(m.vms[index]), nil
	}
	if IsActive(m.vms[index]) {
		return VirtualMachine{}, ErrDeviceRunning
	}
	if !strings.EqualFold(updated.MACAddress, current.MACAddress) && m.macAddressInUseLocked(updated.MACAddress) {
		return VirtualMachine{}, validate.Field("mac_address", "is already assigned to another network interface")
	}
	previous := slices.Clone(m.vms[index].NICs)
	m.vms[index].NICs = slices.Clone(m.vms[index].NICs)
	m.vms[index].NICs[nicIndex] = updated
	if err := m.store.Save(m.vms); err != nil {
		m.vms[index].NICs = previous
		return VirtualMachine{}, err
	}
	return m.annotated(m.vms[index]), nil
}

func (m *Manager) RemoveNIC(id, nicID string) (VirtualMachine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reconcileLocked(); err != nil {
		return VirtualMachine{}, err
	}
	index := m.indexByID(id)
	if index == -1 {
		return VirtualMachine{}, ErrNotFound
	}
	if err := m.requireIdleLocked(id); err != nil {
		return VirtualMachine{}, err
	}
	if IsActive(m.vms[index]) {
		return VirtualMachine{}, ErrDeviceRunning
	}
	nicIndex := slices.IndexFunc(m.vms[index].NICs, func(nic NIC) bool { return nic.ID == nicID })
	if nicIndex == -1 {
		return VirtualMachine{}, validate.Field("nic_id", "network interface was not found")
	}
	previous := slices.Clone(m.vms[index].NICs)
	m.vms[index].NICs = slices.Delete(slices.Clone(m.vms[index].NICs), nicIndex, nicIndex+1)
	if err := m.store.Save(m.vms); err != nil {
		m.vms[index].NICs = previous
		return VirtualMachine{}, err
	}
	return m.annotated(m.vms[index]), nil
}

// newNICLocked allocates an interface identity for a spec, generating a
// stable MAC address when none is requested.
func (m *Manager) newNICLocked(vm VirtualMachine, spec NICSpec) (NIC, error) {
	nicID, err := fsutil.RandomID()
	if err != nil {
		return NIC{}, fmt.Errorf("generate network interface ID: %w", err)
	}
	nic := NIC{ID: nicID, Mode: spec.Mode, BridgeName: spec.BridgeName, MACAddress: spec.MACAddress}
	if nic.MACAddress == "" {
		nic.MACAddress = NICMACAddress(vm.ID, nicID)
	}
	if m.macAddressInUseLocked(nic.MACAddress) || vm.HasMACAddress(nic.MACAddress) {
		return NIC{}, validate.Field("mac_address", "is already assigned to another network interface")
	}
	return nic, nil
}

func (m *Manager) macAddressInUseLocked(macAddress string) bool {
	for _, vm := range m.vms {
		if vm.HasMACAddress(macAddress) {
			return true
		}
	}
	for _, pending := range m.creating {
		if pending.HasMACAddress(macAddress) {
			return true
		}
	}
	return false
}

func normalizeNICSpec(spec NICSpec) (NICSpec, error) {
	spec.BridgeName = strings.TrimSpace(spec.BridgeName)
	spec.MACAddress = strings.TrimSpace(spec.MACAddress)
	switch spec.Mode {
	case NetworkModeNAT:
		spec.BridgeName = ""
	case NetworkModeBridge:
		if err := validate.InterfaceName(spec.BridgeName); err != nil {
			return NICSpec{}, validate.Field("bridge_name", err.Error())
		}
	default:
		return NICSpec{}, validate.Field("mode", "must be NAT or bridge")
	}
	if spec.MACAddress != "" {
		parsed, err := net.ParseMAC(spec.MACAddress)
		if err != nil || len(parsed) != 6 || parsed[0]&1 != 0 {
			return NICSpec{}, validate.Field("mac_address", "must be a valid unicast MAC address")
		}
		spec.MACAddress = parsed.String()
	}
	return spec, nil
}

func (m *Manager) AddDisk(ctx context.Context, id string, spec DiskSpec) (VirtualMachine, error) {
	spec.SourceImage = normalizeStorageName(spec.SourceImage)
	if err := m.validateDiskSpec(spec); err != nil {
		return VirtualMachine{}, err
	}
	m.mu.Lock()
	index := m.indexByID(id)
	if index == -1 {
		m.mu.Unlock()
		return VirtualMachine{}, ErrNotFound
	}
	if IsActive(m.vms[index]) {
		m.mu.Unlock()
		return VirtualMachine{}, ErrDeviceRunning
	}
	if err := m.requireIdleLocked(id); err != nil {
		m.mu.Unlock()
		return VirtualMachine{}, err
	}
	diskID, err := fsutil.RandomID()
	if err != nil {
		m.mu.Unlock()
		return VirtualMachine{}, fmt.Errorf("generate disk ID: %w", err)
	}
	disk := Disk{ID: diskID, SizeGiB: spec.SizeGiB, SourceImage: spec.SourceImage}
	m.busy[id] = "adding a disk"
	m.mu.Unlock()

	createErr := m.createDiskFile(ctx, id, disk)

	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.busy, id)
	if createErr != nil {
		return VirtualMachine{}, createErr
	}
	index = m.indexByID(id)
	if index == -1 {
		_ = fsutil.RemoveIfExists(m.diskPath(id, disk))
		return VirtualMachine{}, ErrNotFound
	}
	previous := slices.Clone(m.vms[index].Disks)
	m.vms[index].Disks = append(slices.Clone(m.vms[index].Disks), disk)
	if err := m.store.Save(m.vms); err != nil {
		m.vms[index].Disks = previous
		_ = fsutil.RemoveIfExists(m.diskPath(id, disk))
		return VirtualMachine{}, err
	}
	return m.annotated(m.vms[index]), nil
}

func (m *Manager) RemoveDisk(id, diskID string) (VirtualMachine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.indexByID(id)
	if index == -1 {
		return VirtualMachine{}, ErrNotFound
	}
	if err := m.requireIdleLocked(id); err != nil {
		return VirtualMachine{}, err
	}
	if IsActive(m.vms[index]) {
		return VirtualMachine{}, ErrDeviceRunning
	}
	diskIndex := slices.IndexFunc(m.vms[index].Disks, func(disk Disk) bool { return disk.ID == diskID })
	if diskIndex == -1 {
		return VirtualMachine{}, validate.Field("disk_id", "disk was not found")
	}
	disk := m.vms[index].Disks[diskIndex]
	path := m.diskPath(id, disk)
	stagedPath := path + ".removing"
	if err := os.Rename(path, stagedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return VirtualMachine{}, fmt.Errorf("stage disk removal: %w", err)
	}
	previous := slices.Clone(m.vms[index].Disks)
	m.vms[index].Disks = slices.Delete(slices.Clone(m.vms[index].Disks), diskIndex, diskIndex+1)
	if err := m.store.Save(m.vms); err != nil {
		m.vms[index].Disks = previous
		_ = os.Rename(stagedPath, path)
		return VirtualMachine{}, err
	}
	if err := fsutil.RemoveIfExists(stagedPath); err != nil {
		return VirtualMachine{}, fmt.Errorf("remove disk: %w", err)
	}
	return m.annotated(m.vms[index]), nil
}

func (m *Manager) AddCDROM(id, isoName string) (VirtualMachine, error) {
	isoName = normalizeStorageName(isoName)
	if err := m.validateOptionalISO(isoName); err != nil {
		return VirtualMachine{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.indexByID(id)
	if index == -1 {
		return VirtualMachine{}, ErrNotFound
	}
	if err := m.requireIdleLocked(id); err != nil {
		return VirtualMachine{}, err
	}
	if IsActive(m.vms[index]) {
		return VirtualMachine{}, ErrDeviceRunning
	}
	cdromID, err := fsutil.RandomID()
	if err != nil {
		return VirtualMachine{}, fmt.Errorf("generate CD-ROM ID: %w", err)
	}
	previous := slices.Clone(m.vms[index].CDROMs)
	m.vms[index].CDROMs = append(m.vms[index].CDROMs, CDROM{ID: cdromID, ISOName: isoName})
	if err := m.store.Save(m.vms); err != nil {
		m.vms[index].CDROMs = previous
		return VirtualMachine{}, err
	}
	return m.annotated(m.vms[index]), nil
}

func (m *Manager) UpdateCDROM(id, cdromID, isoName string) (VirtualMachine, error) {
	isoName = normalizeStorageName(isoName)
	if err := m.validateOptionalISO(isoName); err != nil {
		return VirtualMachine{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.indexByID(id)
	if index == -1 {
		return VirtualMachine{}, ErrNotFound
	}
	if err := m.requireIdleLocked(id); err != nil {
		return VirtualMachine{}, err
	}
	cdromIndex := slices.IndexFunc(m.vms[index].CDROMs, func(cdrom CDROM) bool { return cdrom.ID == cdromID })
	if cdromIndex == -1 {
		return VirtualMachine{}, validate.Field("cdrom_id", "CD-ROM device was not found")
	}
	previousISO := m.vms[index].CDROMs[cdromIndex].ISOName
	if previousISO == isoName {
		return m.annotated(m.vms[index]), nil
	}
	if IsActive(m.vms[index]) {
		path := ""
		if isoName != "" {
			path = m.isoFile(isoName)
		}
		if err := m.hypervisor.ChangeCDROMMedia(m.vms[index], cdromID, path); err != nil {
			return VirtualMachine{}, fmt.Errorf("change live CD-ROM media: %w", err)
		}
	}
	m.vms[index].CDROMs = slices.Clone(m.vms[index].CDROMs)
	m.vms[index].CDROMs[cdromIndex].ISOName = isoName
	if err := m.store.Save(m.vms); err != nil {
		m.vms[index].CDROMs[cdromIndex].ISOName = previousISO
		if IsActive(m.vms[index]) {
			previousPath := ""
			if previousISO != "" {
				previousPath = m.isoFile(previousISO)
			}
			_ = m.hypervisor.ChangeCDROMMedia(m.vms[index], cdromID, previousPath)
		}
		return VirtualMachine{}, err
	}
	return m.annotated(m.vms[index]), nil
}

func (m *Manager) RemoveCDROM(id, cdromID string) (VirtualMachine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.indexByID(id)
	if index == -1 {
		return VirtualMachine{}, ErrNotFound
	}
	if err := m.requireIdleLocked(id); err != nil {
		return VirtualMachine{}, err
	}
	if IsActive(m.vms[index]) {
		return VirtualMachine{}, ErrDeviceRunning
	}
	cdromIndex := slices.IndexFunc(m.vms[index].CDROMs, func(cdrom CDROM) bool { return cdrom.ID == cdromID })
	if cdromIndex == -1 {
		return VirtualMachine{}, validate.Field("cdrom_id", "CD-ROM device was not found")
	}
	previous := slices.Clone(m.vms[index].CDROMs)
	m.vms[index].CDROMs = slices.Delete(slices.Clone(m.vms[index].CDROMs), cdromIndex, cdromIndex+1)
	if err := m.store.Save(m.vms); err != nil {
		m.vms[index].CDROMs = previous
		return VirtualMachine{}, err
	}
	return m.annotated(m.vms[index]), nil
}

func (m *Manager) ConfigureIPMI(id string, config IPMIConfig) (VirtualMachine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.indexByID(id)
	if index == -1 {
		return VirtualMachine{}, ErrNotFound
	}
	if err := m.requireIdleLocked(id); err != nil {
		return VirtualMachine{}, err
	}
	previous := m.vms[index].IPMI
	if !config.Enabled {
		if m.ipmi != nil {
			m.ipmi.Disable(m.vms[index])
		}
		m.vms[index].IPMI = IPMIConfig{}
	} else {
		config.BridgeName = strings.TrimSpace(config.BridgeName)
		config.Address = strings.TrimSpace(config.Address)
		config.Username = strings.TrimSpace(config.Username)
		if config.Password == "" {
			config.Password = previous.Password
		}
		if err := validateIPMIConfig(config); err != nil {
			return VirtualMachine{}, err
		}
		candidate := m.vms[index]
		candidate.IPMI = config
		if m.ipmi == nil {
			return VirtualMachine{}, errors.New("IPMI service is unavailable")
		}
		if err := m.ipmi.Configure(candidate); err != nil {
			return VirtualMachine{}, err
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
		return VirtualMachine{}, err
	}
	return m.annotated(m.vms[index]), nil
}

func validateIPMIConfig(config IPMIConfig) error {
	if err := validate.InterfaceName(config.BridgeName); err != nil {
		return validate.Field("bridge_name", err.Error())
	}
	address, err := netip.ParseAddr(config.Address)
	if err != nil || !address.Is4() || address.IsUnspecified() || address.IsMulticast() {
		return validate.Field("address", "must be a usable IPv4 address")
	}
	if config.Username == "" || len(config.Username) > 16 {
		return validate.Field("username", "must be between 1 and 16 characters")
	}
	if config.Password == "" || len(config.Password) > 20 {
		return validate.Field("password", "must be between 1 and 20 bytes")
	}
	return nil
}

func (m *Manager) SetIPMIBootFlags(id string, device uint8, persistent bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.indexByID(id)
	if index == -1 {
		return ErrNotFound
	}
	m.vms[index].IPMI.BootDevice = device
	m.vms[index].IPMI.BootPersistent = persistent
	return m.store.Save(m.vms)
}

func (m *Manager) Reset(id string) error {
	_, err := m.Reboot(id)
	return err
}

func (m *Manager) Reboot(id string) (VirtualMachine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.reconcileLocked(); err != nil {
		return VirtualMachine{}, err
	}
	index := m.indexByID(id)
	if index == -1 {
		return VirtualMachine{}, ErrNotFound
	}
	if m.vms[index].Status != StatusRunning {
		return VirtualMachine{}, ErrNotRunning
	}
	if err := m.hypervisor.Reset(m.vms[index]); err != nil {
		return VirtualMachine{}, fmt.Errorf("reboot virtual machine: %w", err)
	}
	return m.annotated(m.vms[index]), nil
}

func (m *Manager) ListISOs() ([]ISOImage, error) {
	entries, err := m.listISOStorageImages(".iso")
	if err != nil {
		return nil, err
	}
	images := make([]ISOImage, 0, len(entries))
	for _, entry := range entries {
		images = append(images, ISOImage{Name: entry.Name, SizeBytes: entry.SizeBytes})
	}
	return images, nil
}

func (m *Manager) ListDiskImages() ([]DiskImage, error) {
	return m.listISOStorageImages(".img", ".qcow2")
}

func (m *Manager) listISOStorageImages(extensions ...string) ([]DiskImage, error) {
	allowed := make(map[string]struct{}, len(extensions))
	for _, extension := range extensions {
		allowed[strings.ToLower(extension)] = struct{}{}
	}
	images := make([]DiskImage, 0)
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
		images = append(images, DiskImage{Name: filepath.ToSlash(relativePath), SizeBytes: uint64(info.Size())})
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

func (m *Manager) ConsoleSocket(id string) (string, error) {
	vm, err := m.Get(id)
	if err != nil {
		return "", err
	}
	if vm.Status != StatusRunning && vm.Status != StatusStopping {
		return "", ErrNotRunning
	}
	return m.vncSocket(id), nil
}

func (m *Manager) processExited(id string, expectedPID int, exitErr error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.indexByID(id)
	if index == -1 {
		return
	}
	if m.vms[index].PID == 0 || (expectedPID != 0 && m.vms[index].PID != expectedPID) {
		return
	}
	wasStopping := m.vms[index].Status == StatusStopping
	exitedVM := m.vms[index]
	m.vms[index].PID = 0
	m.vms[index].Status = StatusStopped
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
func (m *Manager) saveLocked() {
	if err := m.store.Save(m.vms); err != nil {
		slog.Error("persist virtual machine definitions", "error", err)
	}
}

func (m *Manager) reconcileLocked() error {
	changed := false
	for index := range m.vms {
		vm := &m.vms[index]
		if vm.Status != StatusRunning && vm.Status != StatusStopping {
			continue
		}
		if vm.PID <= 0 || !m.hypervisor.IsRunning(*vm) {
			stoppedVM := *vm
			vm.PID = 0
			vm.Status = StatusStopped
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

func (m *Manager) validateDiskSpec(spec DiskSpec) error {
	if spec.SizeGiB < 1 || spec.SizeGiB > 2048 {
		return validate.Field("size_gib", "must be between 1 and 2048")
	}
	if spec.SourceImage != "" {
		if _, err := m.resolveISOStorageFile(spec.SourceImage, ".img", ".qcow2"); err != nil {
			return validate.Field("source_image", err.Error())
		}
	}
	return nil
}

func validateCloudInit(configuration CloudInit) error {
	if len(configuration.UserData) > cloudinit.MaxUserDataBytes {
		return validate.Field("cloud_init_user_data", "must not exceed 1 MiB")
	}
	return nil
}

func validateIdentityAndResources(name, description string, cpus, memoryMiB uint32) error {
	if name == "" {
		return validate.Field("name", "a name is required")
	}
	if len(name) > 80 {
		return validate.Field("name", "must not exceed 80 characters")
	}
	if err := validate.DNSLabel(name); err != nil {
		return validate.Field("name", "must be a valid DNS hostname label: "+err.Error())
	}
	if len(description) > 4096 {
		return validate.Field("description", "must not exceed 4096 characters")
	}
	if cpus < 1 || cpus > 64 {
		return validate.Field("cpus", "must be between 1 and 64")
	}
	if memoryMiB < 256 || memoryMiB > 1_048_576 {
		return validate.Field("memory_mib", "must be between 256 and 1048576")
	}
	return nil
}

func (m *Manager) validateOptionalISO(isoName string) error {
	if isoName == "" {
		return nil
	}
	if _, err := m.resolveISOStorageFile(isoName, ".iso"); err != nil {
		return validate.Field("iso_name", err.Error())
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

func (m *Manager) resolveISOStorageFile(name string, extensions ...string) (string, error) {
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
	if !config.RegularFile(resolved) {
		return "", errors.New("the selected file is not a regular file")
	}
	return resolved, nil
}

func IsActive(vm VirtualMachine) bool {
	return vm.Status == StatusRunning || vm.Status == StatusStopping
}

// bridges lists the distinct host bridges the VM's interfaces attach to.
func (vm VirtualMachine) Bridges() []string {
	var result []string
	for _, nic := range vm.NICs {
		if nic.Mode == NetworkModeBridge && nic.BridgeName != "" && !slices.Contains(result, nic.BridgeName) {
			result = append(result, nic.BridgeName)
		}
	}
	return result
}

func (vm VirtualMachine) UsesBridge(bridge string) bool {
	return slices.Contains(vm.Bridges(), bridge)
}

func (vm VirtualMachine) HasMACAddress(macAddress string) bool {
	for _, nic := range vm.NICs {
		if strings.EqualFold(nic.MACAddress, macAddress) {
			return true
		}
	}
	return false
}

func (m *Manager) indexByID(id string) int {
	for index := range m.vms {
		if m.vms[index].ID == id {
			return index
		}
	}
	return -1
}

func (m *Manager) diskPath(vmID string, disk Disk) string {
	return m.settings.VMDiskPath(vmID, disk.ID)
}

func (m *Manager) isoFile(name string) string {
	return m.settings.ISOPath(name)
}

func (m *Manager) qmpSocket(id string) string {
	return m.settings.QMPSocketPath(id)
}

func (m *Manager) vncSocket(id string) string {
	return m.settings.VNCSocketPath(id)
}

func (m *Manager) cloudInitISO(id string) string {
	return m.settings.CloudInitISOPath(id)
}

func (m *Manager) uefiVars(id string) string {
	return m.settings.VMUEFIVarsPath(id)
}

func isoOptions(vm VirtualMachine) cloudinit.ISOOptions {
	return cloudinit.ISOOptions{InstanceID: vm.ID, Hostname: vm.Name, UserData: vm.CloudInit.UserData}
}

func (m *Manager) stageCloudInitISO(ctx context.Context, vm VirtualMachine) (commit func(), rollback func(), err error) {
	if err := os.MkdirAll(m.settings.VMStateDir(vm.ID), 0o750); err != nil {
		return nil, nil, fmt.Errorf("create VM state directory: %w", err)
	}
	path := m.cloudInitISO(vm.ID)
	stagedPath := path + ".new"
	backupPath := path + ".previous"
	for _, stalePath := range []string{stagedPath, backupPath} {
		if err := fsutil.RemoveIfExists(stalePath); err != nil {
			return nil, nil, err
		}
	}
	if vm.CloudInit.Enabled {
		if err := m.buildCloudInitISO(ctx, stagedPath, isoOptions(vm)); err != nil {
			return nil, nil, err
		}
	}
	hadPrevious := false
	if _, statErr := os.Stat(path); statErr == nil {
		if err := os.Rename(path, backupPath); err != nil {
			_ = fsutil.RemoveIfExists(stagedPath)
			return nil, nil, err
		}
		hadPrevious = true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		_ = fsutil.RemoveIfExists(stagedPath)
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
	commit = func() { _ = fsutil.RemoveIfExists(backupPath) }
	rollback = func() {
		_ = fsutil.RemoveIfExists(path)
		if hadPrevious {
			_ = os.Rename(backupPath, path)
		}
		_ = fsutil.RemoveIfExists(stagedPath)
	}
	return commit, rollback, nil
}

// NICMACAddress derives a stable locally administered QEMU-prefixed MAC
// address from the VM and interface identities.
func NICMACAddress(vmID, nicID string) string {
	digest := sha256.Sum256([]byte(vmID + "/" + nicID))
	address := net.HardwareAddr{0x52, 0x54, 0x00, digest[0], digest[1], digest[2]}
	return address.String()
}

func copyFile(ctx context.Context, sourcePath, destinationPath string) error {
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
