// Package ipmi exposes managed VMs as virtual BMCs over IPMI 2.0.
package ipmi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"

	"github.com/bougou/go-ipmi/pkg/bmc"
	"github.com/bougou/go-ipmi/pkg/hal"
	"github.com/bougou/go-ipmi/pkg/server"
	"github.com/bougou/go-ipmi/pkg/transport/udp"
	"github.com/bougou/go-ipmi/pkg/types"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/roflware/app-runner/internal/hostnet"
	"github.com/roflware/app-runner/internal/vm"
)

const Port = 623

type endpoint struct {
	server     *server.Server
	bridge     string
	address    netip.Addr
	prefixBits int
	username   string
	password   string
	running    bool
	lastError  string
}

// serves reports whether a running listener already matches the requested
// endpoint and credentials, in which case reconfiguring would only drop
// active sessions for no change.
func (r *endpoint) serves(config vm.IPMIConfig, address netip.Addr) bool {
	return r.running && r.bridge == config.BridgeName && r.address == address &&
		r.username == config.Username && r.password == config.Password
}

type Manager struct {
	mu              sync.Mutex
	vms             *vm.Manager
	prefixForBridge func(string) (netip.Prefix, bool)
	runtimes        map[string]*endpoint
}

func NewManager(vms *vm.Manager, prefixForBridge func(string) (netip.Prefix, bool)) *Manager {
	return &Manager{vms: vms, prefixForBridge: prefixForBridge, runtimes: make(map[string]*endpoint)}
}

func (m *Manager) Configure(machine vm.VirtualMachine) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !machine.IPMI.Enabled {
		m.disableLocked(machine.ID)
		return nil
	}
	address, _ := netip.ParseAddr(machine.IPMI.Address)
	if runtime := m.runtimes[machine.ID]; runtime != nil && runtime.serves(machine.IPMI, address) {
		return nil
	}
	prefix, err := m.bridgePrefix(machine.IPMI.BridgeName, address)
	if err != nil {
		return err
	}
	for id, runtime := range m.runtimes {
		if id != machine.ID && runtime.address == address {
			return fmt.Errorf("IPMI address %s is already used by another virtual machine", address)
		}
	}
	m.disableLocked(machine.ID)
	if err := ensureAddress(machine.IPMI.BridgeName, prefix); err != nil {
		return err
	}
	connection, err := udp.Listen(net.JoinHostPort(address.String(), fmt.Sprint(Port)))
	if err != nil {
		_ = removeAddress(machine.IPMI.BridgeName, prefix)
		return fmt.Errorf("start IPMI listener: %w", err)
	}
	hardware := &chassisHAL{manager: m.vms, vmID: machine.ID}
	var guid [16]byte
	copy(guid[:], []byte(machine.ID))
	device := bmc.New(bmc.DeviceInfo{
		DeviceID: 32, DeviceRevision: 1, FirmwareMajor: 1, FirmwareMinor: 0,
		IPMIVersion: 0x20, ManufacturerID: 0x00FFFFFF, ProductID: 1,
	}, guid, hardware)
	user, err := device.Users.Add(2, machine.IPMI.Username)
	if err != nil {
		_ = connection.Close()
		_ = removeAddress(machine.IPMI.BridgeName, prefix)
		return fmt.Errorf("configure IPMI user: %w", err)
	}
	user.SetPassword([]byte(machine.IPMI.Password))
	user.Enabled = true
	user.ChannelAccess[1] = bmc.UserChannelAccess{MaxPrivilege: bmc.PrivilegeLevelAdministrator, Enabled: true}
	runtime := &endpoint{
		bridge: machine.IPMI.BridgeName, address: address, prefixBits: prefix.Bits(),
		username: machine.IPMI.Username, password: machine.IPMI.Password, running: true,
	}
	runtime.server = server.NewServer(device, connection, server.WithCipherSuites([]types.CipherSuiteID{types.CipherSuiteID17}), server.WithV15Disabled())
	m.runtimes[machine.ID] = runtime
	go func() {
		err := runtime.server.Serve(context.Background())
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.runtimes[machine.ID] != runtime {
			return
		}
		runtime.running = false
		if err != nil && !errors.Is(err, context.Canceled) {
			runtime.lastError = err.Error()
			slog.Error("IPMI server stopped unexpectedly", "machine", machine.Name, "address", address, "error", err)
		}
	}()
	slog.Info("IPMI server started", "machine", machine.Name, "bridge", machine.IPMI.BridgeName, "address", address, "port", Port)
	return nil
}

func (m *Manager) Disable(machine vm.VirtualMachine) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disableLocked(machine.ID)
}

func (m *Manager) disableLocked(id string) {
	runtime := m.runtimes[id]
	if runtime == nil {
		return
	}
	delete(m.runtimes, id)
	_ = runtime.server.Close()
	prefix := netip.PrefixFrom(runtime.address, runtime.prefixBits)
	if err := removeAddress(runtime.bridge, prefix); err != nil {
		slog.Warn("remove IPMI bridge address", "bridge", runtime.bridge, "address", runtime.address, "error", err)
	}
}

func (m *Manager) Status(id string) (bool, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	runtime := m.runtimes[id]
	if runtime == nil {
		return false, ""
	}
	return runtime.running, runtime.lastError
}

func (m *Manager) Reconcile(vms []vm.VirtualMachine) error {
	var result error
	for _, machine := range vms {
		if machine.IPMI.Enabled {
			if address, err := netip.ParseAddr(machine.IPMI.Address); err == nil {
				if prefix, prefixErr := m.bridgePrefix(machine.IPMI.BridgeName, address); prefixErr == nil {
					_ = removeAddress(machine.IPMI.BridgeName, prefix)
				}
			}
			if err := m.Configure(machine); err != nil {
				result = errors.Join(result, fmt.Errorf("restore IPMI for VM %s: %w", machine.Name, err))
			}
		}
	}
	return result
}

func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.runtimes {
		m.disableLocked(id)
	}
}

func (m *Manager) bridgePrefix(bridge string, address netip.Addr) (netip.Prefix, error) {
	link, err := hostnet.RequireBridge(bridge)
	if err != nil {
		return netip.Prefix{}, err
	}
	addresses, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("inspect bridge addresses: %w", err)
	}
	for _, existing := range addresses {
		if existing.IPNet == nil {
			continue
		}
		prefix, parseErr := netip.ParsePrefix(existing.IPNet.String())
		if parseErr == nil && prefix.Contains(address) {
			return netip.PrefixFrom(address, prefix.Bits()), nil
		}
	}
	if m.prefixForBridge != nil {
		if prefix, found := m.prefixForBridge(bridge); found && prefix.Contains(address) {
			return netip.PrefixFrom(address, prefix.Bits()), nil
		}
	}
	return netip.Prefix{}, fmt.Errorf("IPMI address %s is not within an IPv4 subnet configured for bridge %s", address, bridge)
}

func ensureAddress(bridge string, prefix netip.Prefix) error {
	link, err := hostnet.RequireBridge(bridge)
	if err != nil {
		return err
	}
	all, err := netlink.AddrList(nil, netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("inspect IPMI address conflicts: %w", err)
	}
	for _, existing := range all {
		if existing.IP != nil && existing.IP.String() == prefix.Addr().String() {
			return fmt.Errorf("IPMI address %s is already assigned to a network interface", prefix.Addr())
		}
	}
	address, err := netlink.ParseAddr(prefix.String())
	if err != nil {
		return err
	}
	if err := netlink.AddrAdd(link, address); err != nil {
		return fmt.Errorf("assign IPMI address %s to bridge %s: %w", prefix.Addr(), bridge, err)
	}
	return nil
}

func removeAddress(bridge string, prefix netip.Prefix) error {
	link, err := hostnet.RequireBridge(bridge)
	if err != nil {
		return err
	}
	address, err := netlink.ParseAddr(prefix.String())
	if err != nil {
		return err
	}
	if err := netlink.AddrDel(link, address); err != nil && !errors.Is(err, unix.EADDRNOTAVAIL) {
		return err
	}
	return nil
}

type chassisHAL struct {
	manager *vm.Manager
	vmID    string
	mu      sync.Mutex
	bootAck types.BootOptionParam_BootInfoAcknowledge
}

func (h *chassisHAL) Chassis() hal.ChassisHAL { return h }
func (h *chassisHAL) Sensors() hal.SensorHAL  { return nil }
func (h *chassisHAL) Storage() hal.StorageHAL { return nil }
func (h *chassisHAL) Network() hal.NetworkHAL { return nil }
func (h *chassisHAL) GPIO() hal.GPIOHAL       { return nil }
func (h *chassisHAL) I2C() hal.I2CHAL         { return nil }
func (h *chassisHAL) Close() error            { return nil }

func (h *chassisHAL) PowerState(context.Context) (bool, error) {
	machine, err := h.manager.Get(h.vmID)
	return machine.Status == vm.StatusRunning || machine.Status == vm.StatusStopping, err
}
func (h *chassisHAL) SetPower(_ context.Context, on bool) error {
	machine, err := h.manager.Get(h.vmID)
	if err != nil {
		return err
	}
	if on {
		if machine.Status == vm.StatusRunning || machine.Status == vm.StatusStopping {
			return nil
		}
		_, err = h.manager.Start(h.vmID)
		return err
	}
	if machine.Status == vm.StatusStopped {
		return nil
	}
	_, err = h.manager.Stop(h.vmID, true, 0)
	return err
}
func (h *chassisHAL) PowerCycle(ctx context.Context) error {
	if err := h.SetPower(ctx, false); err != nil {
		return err
	}
	return h.SetPower(ctx, true)
}
func (h *chassisHAL) ColdReset(context.Context) error { return h.manager.Reset(h.vmID) }
func (h *chassisHAL) WarmReset(_ context.Context) error {
	_, err := h.manager.Stop(h.vmID, false, 0)
	return err
}
func (h *chassisHAL) Identify(context.Context, uint8) error        { return nil }
func (h *chassisHAL) IntrusionState(context.Context) (bool, error) { return false, nil }
func (h *chassisHAL) SetBootFlags(_ context.Context, flags *types.BootOptionParam_BootFlags) error {
	switch flags.BootDeviceSelector {
	case types.BootDeviceSelectorNoOverride, types.BootDeviceSelectorForcePXE,
		types.BootDeviceSelectorForceHardDrive, types.BootDeviceSelectorForceCDROM:
	default:
		return hal.ErrNotSupported
	}
	return h.manager.SetIPMIBootFlags(h.vmID, uint8(flags.BootDeviceSelector), flags.Persist)
}
func (h *chassisHAL) GetBootFlags(context.Context) (*types.BootOptionParam_BootFlags, error) {
	machine, err := h.manager.Get(h.vmID)
	if err != nil {
		return nil, err
	}
	return &types.BootOptionParam_BootFlags{BootFlagsValid: machine.IPMI.BootDevice != 0, Persist: machine.IPMI.BootPersistent, BootDeviceSelector: types.BootDeviceSelector(machine.IPMI.BootDevice)}, nil
}
func (h *chassisHAL) SetBootInfoAcknowledge(_ context.Context, ack *types.BootOptionParam_BootInfoAcknowledge) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.bootAck = *ack
	return nil
}
func (h *chassisHAL) GetBootInfoAcknowledge(context.Context) (*types.BootOptionParam_BootInfoAcknowledge, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ack := h.bootAck
	return &ack, nil
}
