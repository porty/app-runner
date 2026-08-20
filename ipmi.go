package main

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
)

const ipmiPort = 623

type ipmiRuntime struct {
	server     *server.Server
	bridge     string
	address    netip.Addr
	prefixBits int
	running    bool
	lastError  string
}

type ipmiManager struct {
	mu              sync.Mutex
	vms             *vmManager
	prefixForBridge func(string) (netip.Prefix, bool)
	runtimes        map[string]*ipmiRuntime
}

func newIPMIManager(vms *vmManager, prefixForBridge func(string) (netip.Prefix, bool)) *ipmiManager {
	return &ipmiManager{vms: vms, prefixForBridge: prefixForBridge, runtimes: make(map[string]*ipmiRuntime)}
}

func (m *ipmiManager) Configure(vm virtualMachine) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !vm.IPMI.Enabled {
		m.disableLocked(vm.ID)
		return nil
	}
	address, _ := netip.ParseAddr(vm.IPMI.Address)
	prefix, err := m.bridgePrefix(vm.IPMI.BridgeName, address)
	if err != nil {
		return err
	}
	for id, runtime := range m.runtimes {
		if id != vm.ID && runtime.address == address {
			return fmt.Errorf("IPMI address %s is already used by another virtual machine", address)
		}
	}
	m.disableLocked(vm.ID)
	if err := ensureIPMIAddress(vm.IPMI.BridgeName, prefix); err != nil {
		return err
	}
	connection, err := udp.Listen(net.JoinHostPort(address.String(), fmt.Sprint(ipmiPort)))
	if err != nil {
		_ = removeIPMIAddress(vm.IPMI.BridgeName, prefix)
		return fmt.Errorf("start IPMI listener: %w", err)
	}
	hardware := &vmIPMIHAL{manager: m.vms, vmID: vm.ID}
	var guid [16]byte
	copy(guid[:], []byte(vm.ID))
	device := bmc.New(bmc.DeviceInfo{
		DeviceID: 32, DeviceRevision: 1, FirmwareMajor: 1, FirmwareMinor: 0,
		IPMIVersion: 0x20, ManufacturerID: 0x00FFFFFF, ProductID: 1,
	}, guid, hardware)
	user, err := device.Users.Add(2, vm.IPMI.Username)
	if err != nil {
		_ = connection.Close()
		_ = removeIPMIAddress(vm.IPMI.BridgeName, prefix)
		return fmt.Errorf("configure IPMI user: %w", err)
	}
	user.SetPassword([]byte(vm.IPMI.Password))
	user.Enabled = true
	user.ChannelAccess[1] = bmc.UserChannelAccess{MaxPrivilege: bmc.PrivilegeLevelAdministrator, Enabled: true}
	runtime := &ipmiRuntime{bridge: vm.IPMI.BridgeName, address: address, prefixBits: prefix.Bits(), running: true}
	runtime.server = server.NewServer(device, connection, server.WithCipherSuites([]types.CipherSuiteID{types.CipherSuiteID17}), server.WithV15Disabled())
	m.runtimes[vm.ID] = runtime
	go func() {
		err := runtime.server.Serve(context.Background())
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.runtimes[vm.ID] != runtime {
			return
		}
		runtime.running = false
		if err != nil && !errors.Is(err, context.Canceled) {
			runtime.lastError = err.Error()
			slog.Error("IPMI server stopped unexpectedly", "vm", vm.Name, "address", address, "error", err)
		}
	}()
	slog.Info("IPMI server started", "vm", vm.Name, "bridge", vm.IPMI.BridgeName, "address", address, "port", ipmiPort)
	return nil
}

func (m *ipmiManager) Disable(vm virtualMachine) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disableLocked(vm.ID)
}

func (m *ipmiManager) disableLocked(id string) {
	runtime := m.runtimes[id]
	if runtime == nil {
		return
	}
	delete(m.runtimes, id)
	_ = runtime.server.Close()
	prefix := netip.PrefixFrom(runtime.address, runtime.prefixBits)
	if err := removeIPMIAddress(runtime.bridge, prefix); err != nil {
		slog.Warn("remove IPMI bridge address", "bridge", runtime.bridge, "address", runtime.address, "error", err)
	}
}

func (m *ipmiManager) Status(id string) (bool, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	runtime := m.runtimes[id]
	if runtime == nil {
		return false, ""
	}
	return runtime.running, runtime.lastError
}

func (m *ipmiManager) Reconcile(vms []virtualMachine) error {
	var result error
	for _, vm := range vms {
		if vm.IPMI.Enabled {
			if address, err := netip.ParseAddr(vm.IPMI.Address); err == nil {
				if prefix, prefixErr := m.bridgePrefix(vm.IPMI.BridgeName, address); prefixErr == nil {
					_ = removeIPMIAddress(vm.IPMI.BridgeName, prefix)
				}
			}
			if err := m.Configure(vm); err != nil {
				result = errors.Join(result, fmt.Errorf("restore IPMI for VM %s: %w", vm.Name, err))
			}
		}
	}
	return result
}

func (m *ipmiManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.runtimes {
		m.disableLocked(id)
	}
}

func (m *ipmiManager) bridgePrefix(bridge string, address netip.Addr) (netip.Prefix, error) {
	link, err := requireBridge(bridge)
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

func ensureIPMIAddress(bridge string, prefix netip.Prefix) error {
	link, err := requireBridge(bridge)
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

func removeIPMIAddress(bridge string, prefix netip.Prefix) error {
	link, err := requireBridge(bridge)
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

type vmIPMIHAL struct {
	manager *vmManager
	vmID    string
	mu      sync.Mutex
	bootAck types.BootOptionParam_BootInfoAcknowledge
}

func (h *vmIPMIHAL) Chassis() hal.ChassisHAL { return h }
func (h *vmIPMIHAL) Sensors() hal.SensorHAL  { return nil }
func (h *vmIPMIHAL) Storage() hal.StorageHAL { return nil }
func (h *vmIPMIHAL) Network() hal.NetworkHAL { return nil }
func (h *vmIPMIHAL) GPIO() hal.GPIOHAL       { return nil }
func (h *vmIPMIHAL) I2C() hal.I2CHAL         { return nil }
func (h *vmIPMIHAL) Close() error            { return nil }

func (h *vmIPMIHAL) PowerState(context.Context) (bool, error) {
	vm, err := h.manager.Get(h.vmID)
	return vm.Status == vmStatusRunning || vm.Status == vmStatusStopping, err
}
func (h *vmIPMIHAL) SetPower(_ context.Context, on bool) error {
	vm, err := h.manager.Get(h.vmID)
	if err != nil {
		return err
	}
	if on {
		if vm.Status == vmStatusRunning || vm.Status == vmStatusStopping {
			return nil
		}
		_, err = h.manager.Start(h.vmID)
		return err
	}
	if vm.Status == vmStatusStopped {
		return nil
	}
	_, err = h.manager.Stop(h.vmID, true)
	return err
}
func (h *vmIPMIHAL) PowerCycle(ctx context.Context) error {
	if err := h.SetPower(ctx, false); err != nil {
		return err
	}
	return h.SetPower(ctx, true)
}
func (h *vmIPMIHAL) ColdReset(context.Context) error { return h.manager.Reset(h.vmID) }
func (h *vmIPMIHAL) WarmReset(_ context.Context) error {
	_, err := h.manager.Stop(h.vmID, false)
	return err
}
func (h *vmIPMIHAL) Identify(context.Context, uint8) error        { return nil }
func (h *vmIPMIHAL) IntrusionState(context.Context) (bool, error) { return false, nil }
func (h *vmIPMIHAL) SetBootFlags(_ context.Context, flags *types.BootOptionParam_BootFlags) error {
	switch flags.BootDeviceSelector {
	case types.BootDeviceSelectorNoOverride, types.BootDeviceSelectorForcePXE,
		types.BootDeviceSelectorForceHardDrive, types.BootDeviceSelectorForceCDROM:
	default:
		return hal.ErrNotSupported
	}
	return h.manager.SetIPMIBootFlags(h.vmID, uint8(flags.BootDeviceSelector), flags.Persist)
}
func (h *vmIPMIHAL) GetBootFlags(context.Context) (*types.BootOptionParam_BootFlags, error) {
	vm, err := h.manager.Get(h.vmID)
	if err != nil {
		return nil, err
	}
	return &types.BootOptionParam_BootFlags{BootFlagsValid: vm.IPMI.BootDevice != 0, Persist: vm.IPMI.BootPersistent, BootDeviceSelector: types.BootDeviceSelector(vm.IPMI.BootDevice)}, nil
}
func (h *vmIPMIHAL) SetBootInfoAcknowledge(_ context.Context, ack *types.BootOptionParam_BootInfoAcknowledge) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.bootAck = *ack
	return nil
}
func (h *vmIPMIHAL) GetBootInfoAcknowledge(context.Context) (*types.BootOptionParam_BootInfoAcknowledge, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ack := h.bootAck
	return &ack, nil
}
