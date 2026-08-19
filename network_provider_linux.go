package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const capNetAdmin = 12

type linuxNetworkProvider struct{}

func newLinuxNetworkProvider() *linuxNetworkProvider {
	return &linuxNetworkProvider{}
}

type networkAccessDiagnostics struct {
	identity           userIdentity
	canManage          bool
	tunAvailable       bool
	helperAvailable    bool
	helperExecutable   bool
	helperPath         string
	bridgeConfig       string
	bridgeConfigLoaded bool
	diagnostics        []networkDiagnostic
}

func (p *linuxNetworkProvider) Inspect() (networkingStatus, error) {
	access := inspectNetworkAccess()
	links, err := netlink.LinkList()
	if err != nil {
		return networkingStatus{}, fmt.Errorf("list network interfaces: %w", err)
	}
	byIndex := make(map[int]netlink.Link, len(links))
	for _, link := range links {
		byIndex[link.Attrs().Index] = link
	}

	status := networkingStatus{
		User: access.identity, Diagnostics: access.diagnostics, CanManage: access.canManage,
		Interfaces: make([]networkInterfaceInfo, 0, len(links)),
	}
	for _, link := range links {
		attributes := link.Attrs()
		addresses, err := linkAddressStrings(link)
		if err != nil {
			return networkingStatus{}, err
		}
		master := ""
		if attributes.MasterIndex != 0 {
			if masterLink, found := byIndex[attributes.MasterIndex]; found {
				master = masterLink.Attrs().Name
			}
		}
		isBridge := link.Type() == "bridge"
		status.Interfaces = append(status.Interfaces, networkInterfaceInfo{
			Name: attributes.Name, IsUp: attributes.Flags&net.FlagUp != 0, MTU: uint32(attributes.MTU),
			HardwareAddress: attributes.HardwareAddr.String(), Addresses: addresses,
			Master: master, IsBridge: isBridge,
			CanAttach: !isBridge && attributes.Name != "lo" && attributes.MasterIndex == 0,
		})
		if !isBridge {
			continue
		}
		bridge := networkBridgeInfo{
			Name: attributes.Name, IsUp: attributes.Flags&net.FlagUp != 0, MTU: uint32(attributes.MTU),
			HardwareAddress: attributes.HardwareAddr.String(), Addresses: addresses,
		}
		for _, member := range links {
			if member.Attrs().MasterIndex == attributes.Index {
				bridge.MemberInterfaces = append(bridge.MemberInterfaces, member.Attrs().Name)
			}
		}
		sort.Strings(bridge.MemberInterfaces)
		bridge.Diagnostics, bridge.UsableByQEMU = bridgeDiagnostics(access, bridge)
		status.Bridges = append(status.Bridges, bridge)
	}
	sort.Slice(status.Interfaces, func(left, right int) bool { return status.Interfaces[left].Name < status.Interfaces[right].Name })
	sort.Slice(status.Bridges, func(left, right int) bool { return status.Bridges[left].Name < status.Bridges[right].Name })
	return status, nil
}

func (p *linuxNetworkProvider) BridgeCapability(name string) (bool, string) {
	status, err := p.Inspect()
	if err != nil {
		return false, err.Error()
	}
	for _, bridge := range status.Bridges {
		if bridge.Name != name {
			continue
		}
		if bridge.UsableByQEMU {
			return true, ""
		}
		for _, diagnostic := range bridge.Diagnostics {
			if diagnostic.Status == diagnosticFail {
				return false, diagnostic.Detail + " " + diagnostic.Remediation
			}
		}
		return false, fmt.Sprintf("bridge %s is not usable by QEMU", name)
	}
	return false, fmt.Sprintf("bridge %s does not exist", name)
}

func (p *linuxNetworkProvider) Snapshot(change networkChange) (networkSnapshot, error) {
	snapshot := networkSnapshot{Change: change}
	add := func(name string, mustExist bool) error {
		link, err := netlink.LinkByName(name)
		if isLinkNotFound(err) {
			if mustExist {
				return fmt.Errorf("network interface %s does not exist", name)
			}
			snapshot.Links = append(snapshot.Links, linkSnapshot{Name: name, Exists: false})
			return nil
		}
		if err != nil {
			return err
		}
		if !mustExist {
			return fmt.Errorf("network interface %s already exists", name)
		}
		captured, err := snapshotLink(link)
		if err != nil {
			return err
		}
		snapshot.Links = append(snapshot.Links, captured)
		return nil
	}

	switch change.Type {
	case networkChangeCreateBridge:
		if err := add(change.BridgeName, false); err != nil {
			return networkSnapshot{}, err
		}
	case networkChangeDeleteBridge, networkChangeSetBridgeUp, networkChangeSetBridgeDown:
		if err := add(change.BridgeName, true); err != nil {
			return networkSnapshot{}, err
		}
	case networkChangeAttachInterface, networkChangeDetachInterface:
		if err := add(change.BridgeName, true); err != nil {
			return networkSnapshot{}, err
		}
		if err := add(change.InterfaceName, true); err != nil {
			return networkSnapshot{}, err
		}
	default:
		return networkSnapshot{}, errors.New("unsupported network change")
	}
	return snapshot, nil
}

func (p *linuxNetworkProvider) Apply(change networkChange) error {
	access := inspectNetworkAccess()
	if !access.canManage {
		return errors.New("network changes require root or CAP_NET_ADMIN; grant only the App Runner executable CAP_NET_ADMIN or run it as root")
	}
	switch change.Type {
	case networkChangeCreateBridge:
		bridge := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: change.BridgeName}}
		if err := netlink.LinkAdd(bridge); err != nil {
			return fmt.Errorf("create bridge: %w", err)
		}
		if err := netlink.LinkSetUp(bridge); err != nil {
			return fmt.Errorf("bring new bridge up: %w", err)
		}
		return nil
	case networkChangeDeleteBridge:
		bridge, err := requireBridge(change.BridgeName)
		if err != nil {
			return err
		}
		members, err := bridgeMembers(bridge)
		if err != nil {
			return err
		}
		if len(members) != 0 {
			return fmt.Errorf("detach bridge members before deleting %s: %s", change.BridgeName, strings.Join(members, ", "))
		}
		return netlink.LinkDel(bridge)
	case networkChangeSetBridgeUp, networkChangeSetBridgeDown:
		bridge, err := requireBridge(change.BridgeName)
		if err != nil {
			return err
		}
		if change.Type == networkChangeSetBridgeUp {
			return netlink.LinkSetUp(bridge)
		}
		return netlink.LinkSetDown(bridge)
	case networkChangeAttachInterface:
		bridge, err := requireBridge(change.BridgeName)
		if err != nil {
			return err
		}
		member, err := netlink.LinkByName(change.InterfaceName)
		if err != nil {
			return fmt.Errorf("find interface: %w", err)
		}
		if member.Type() == "bridge" || member.Attrs().Name == "lo" {
			return errors.New("bridges and loopback cannot be attached as bridge members")
		}
		if member.Attrs().MasterIndex != 0 {
			return fmt.Errorf("interface %s already belongs to another master", change.InterfaceName)
		}
		if err := netlink.LinkSetMaster(member, bridge); err != nil {
			return fmt.Errorf("attach interface: %w", err)
		}
		if change.MigrateAddresses {
			return moveNetworkConfiguration(member, bridge)
		}
		return nil
	case networkChangeDetachInterface:
		bridge, err := requireBridge(change.BridgeName)
		if err != nil {
			return err
		}
		member, err := netlink.LinkByName(change.InterfaceName)
		if err != nil {
			return fmt.Errorf("find interface: %w", err)
		}
		if member.Attrs().MasterIndex != bridge.Attrs().Index {
			return fmt.Errorf("interface %s is not attached to %s", change.InterfaceName, change.BridgeName)
		}
		if change.MigrateAddresses {
			members, err := bridgeMembers(bridge)
			if err != nil {
				return err
			}
			if len(members) != 1 {
				return errors.New("addresses can only be migrated off a bridge with one member")
			}
			if err := moveNetworkConfiguration(bridge, member); err != nil {
				return err
			}
		}
		return netlink.LinkSetNoMaster(member)
	default:
		return errors.New("unsupported network change")
	}
}

func (p *linuxNetworkProvider) Restore(snapshot networkSnapshot) error {
	for _, saved := range snapshot.Links {
		current, err := netlink.LinkByName(saved.Name)
		missing := isLinkNotFound(err)
		if !saved.Exists {
			if err == nil {
				if deleteErr := netlink.LinkDel(current); deleteErr != nil {
					return fmt.Errorf("remove created interface %s: %w", saved.Name, deleteErr)
				}
			}
			continue
		}
		if missing {
			if !saved.IsBridge {
				return fmt.Errorf("cannot restore missing interface %s", saved.Name)
			}
			bridge := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: saved.Name}}
			if addErr := netlink.LinkAdd(bridge); addErr != nil {
				return fmt.Errorf("recreate bridge %s: %w", saved.Name, addErr)
			}
			current = bridge
		} else if err != nil {
			return err
		}
		if saved.MTU > 0 && current.Attrs().MTU != saved.MTU {
			if err := netlink.LinkSetMTU(current, saved.MTU); err != nil {
				return fmt.Errorf("restore MTU on %s: %w", saved.Name, err)
			}
		}
	}

	for _, saved := range snapshot.Links {
		if !saved.Exists {
			continue
		}
		link, err := netlink.LinkByName(saved.Name)
		if err != nil {
			return err
		}
		if saved.Master == "" {
			if link.Attrs().MasterIndex != 0 {
				if err := netlink.LinkSetNoMaster(link); err != nil {
					return fmt.Errorf("detach %s during rollback: %w", saved.Name, err)
				}
			}
		} else {
			master, err := netlink.LinkByName(saved.Master)
			if err != nil {
				return fmt.Errorf("find previous master %s: %w", saved.Master, err)
			}
			if err := netlink.LinkSetMaster(link, master); err != nil {
				return fmt.Errorf("restore master for %s: %w", saved.Name, err)
			}
		}
		if err := restoreNetworkConfiguration(link, saved); err != nil {
			return err
		}
		if saved.IsUp {
			err = netlink.LinkSetUp(link)
		} else {
			err = netlink.LinkSetDown(link)
		}
		if err != nil {
			return fmt.Errorf("restore link state for %s: %w", saved.Name, err)
		}
	}
	return nil
}

func inspectNetworkAccess() networkAccessDiagnostics {
	identity := currentUserIdentity()
	result := networkAccessDiagnostics{identity: identity, canManage: identity.IsRoot || identity.HasCAPNetAdmin}
	manageDiagnostic := networkDiagnostic{Key: "cap_net_admin", Label: "Bridge modification permission"}
	if result.canManage {
		manageDiagnostic.Status = diagnosticPass
		manageDiagnostic.Detail = "The backend has CAP_NET_ADMIN and can modify Linux bridges."
	} else {
		manageDiagnostic.Status = diagnosticFail
		manageDiagnostic.Detail = "The backend does not have CAP_NET_ADMIN. Read-only diagnostics remain available."
		manageDiagnostic.Remediation = "Run App Runner as root, or grant CAP_NET_ADMIN only to the production executable with: sudo setcap cap_net_admin=+ep ./bin/app-runner"
	}
	result.diagnostics = append(result.diagnostics, manageDiagnostic)

	tunDiagnostic := networkDiagnostic{Key: "tun_access", Label: "/dev/net/tun read/write access"}
	if info, err := os.Stat("/dev/net/tun"); err != nil {
		tunDiagnostic.Status = diagnosticFail
		tunDiagnostic.Detail = fmt.Sprintf("/dev/net/tun cannot be inspected: %v", err)
		tunDiagnostic.Remediation = "Load the tun kernel module and ensure /dev/net/tun exists."
	} else {
		owner, group := fileOwnerNames(info)
		file, openErr := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
		if openErr == nil {
			result.tunAvailable = true
			_ = file.Close()
			tunDiagnostic.Status = diagnosticPass
			tunDiagnostic.Detail = fmt.Sprintf("Accessible as %s:%s with mode %s.", owner, group, info.Mode().Perm())
		} else {
			tunDiagnostic.Status = diagnosticFail
			tunDiagnostic.Detail = fmt.Sprintf("Present as %s:%s with mode %s, but opening read/write failed: %v", owner, group, info.Mode().Perm(), openErr)
			tunDiagnostic.Remediation = fmt.Sprintf("Add %s to the device-owning group %s or configure an ACL granting read/write access, then start a new login session.", identity.Username, group)
		}
	}
	result.diagnostics = append(result.diagnostics, tunDiagnostic)

	helperDiagnostic := networkDiagnostic{Key: "qemu_bridge_helper", Label: "qemu-bridge-helper access"}
	result.helperPath = locateQEMUBridgeHelper()
	if result.helperPath == "" {
		helperDiagnostic.Status = diagnosticFail
		helperDiagnostic.Detail = "qemu-bridge-helper was not found in PATH or a standard QEMU library directory."
		helperDiagnostic.Remediation = "Install the distribution package that provides qemu-bridge-helper."
	} else if info, err := os.Stat(result.helperPath); err != nil {
		helperDiagnostic.Status = diagnosticFail
		helperDiagnostic.Detail = err.Error()
	} else {
		result.helperAvailable = true
		result.helperExecutable = unix.Access(result.helperPath, unix.X_OK) == nil
		owner, group := fileOwnerNames(info)
		setuid := info.Mode()&os.ModeSetuid != 0
		capabilities := fileHasCapabilities(result.helperPath)
		helperDiagnostic.Detail = fmt.Sprintf("%s is owned by %s:%s, mode %s, executable=%t, setuid=%t, file-capabilities=%t.", result.helperPath, owner, group, info.Mode().Perm(), result.helperExecutable, setuid, capabilities)
		if result.helperExecutable && (identity.IsRoot || setuid || capabilities) {
			helperDiagnostic.Status = diagnosticPass
		} else {
			helperDiagnostic.Status = diagnosticFail
			helperDiagnostic.Remediation = "Restore the distribution-provided qemu-bridge-helper setuid permissions or grant CAP_NET_ADMIN to the helper itself, or run App Runner as root. Do not make the helper world-writable."
		}
	}
	result.diagnostics = append(result.diagnostics, helperDiagnostic)

	configDiagnostic := networkDiagnostic{Key: "qemu_bridge_config", Label: "QEMU bridge allow-list"}
	configuration, err := os.ReadFile("/etc/qemu/bridge.conf")
	if err != nil {
		configDiagnostic.Status = diagnosticFail
		configDiagnostic.Detail = fmt.Sprintf("/etc/qemu/bridge.conf is not readable: %v", err)
		configDiagnostic.Remediation = "Create a root-owned, readable /etc/qemu/bridge.conf with an explicit 'allow BRIDGE_NAME' line for each permitted bridge."
	} else {
		result.bridgeConfig = string(configuration)
		result.bridgeConfigLoaded = true
		configDiagnostic.Status = diagnosticPass
		configDiagnostic.Detail = "/etc/qemu/bridge.conf is readable; each bridge is evaluated separately below."
	}
	result.diagnostics = append(result.diagnostics, configDiagnostic)
	return result
}

func bridgeDiagnostics(access networkAccessDiagnostics, bridge networkBridgeInfo) ([]networkDiagnostic, bool) {
	diagnostics := []networkDiagnostic{{Key: "bridge_state", Label: "Bridge operational state", Status: diagnosticPass, Detail: fmt.Sprintf("Bridge %s exists and is %s.", bridge.Name, map[bool]string{true: "up", false: "down"}[bridge.IsUp])}}
	if !bridge.IsUp {
		diagnostics[0].Status = diagnosticWarning
		diagnostics[0].Remediation = "Bring the bridge up before starting attached workloads."
	}
	allowed := access.bridgeConfigLoaded && bridgeConfigurationAllows(access.bridgeConfig, bridge.Name)
	configDiagnostic := networkDiagnostic{Key: "bridge_allowed", Label: "QEMU allow-list entry"}
	if allowed {
		configDiagnostic.Status = diagnosticPass
		configDiagnostic.Detail = fmt.Sprintf("/etc/qemu/bridge.conf allows %s.", bridge.Name)
	} else {
		configDiagnostic.Status = diagnosticFail
		configDiagnostic.Detail = fmt.Sprintf("/etc/qemu/bridge.conf does not allow %s.", bridge.Name)
		configDiagnostic.Remediation = fmt.Sprintf("Add 'allow %s' to /etc/qemu/bridge.conf as root.", bridge.Name)
	}
	diagnostics = append(diagnostics, configDiagnostic)
	usable := bridge.IsUp && access.tunAvailable && access.helperAvailable && access.helperExecutable && allowed
	return diagnostics, usable
}

func currentUserIdentity() userIdentity {
	identity := userIdentity{IsRoot: os.Geteuid() == 0, HasCAPNetAdmin: effectiveCapability(capNetAdmin)}
	current, err := user.Current()
	if err != nil {
		identity.Username = strconv.Itoa(os.Geteuid())
		identity.UID = uint32(os.Geteuid())
		return identity
	}
	identity.Username = current.Username
	uid, _ := strconv.ParseUint(current.Uid, 10, 32)
	identity.UID = uint32(uid)
	groupIDs, _ := current.GroupIds()
	for _, groupID := range groupIDs {
		group, lookupErr := user.LookupGroupId(groupID)
		if lookupErr == nil {
			identity.Groups = append(identity.Groups, group.Name)
		} else {
			identity.Groups = append(identity.Groups, groupID)
		}
	}
	sort.Strings(identity.Groups)
	return identity
}

func effectiveCapability(capability uint) bool {
	contents, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(contents), "\n") {
		if !strings.HasPrefix(line, "CapEff:") {
			continue
		}
		value, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(line, "CapEff:")), 16, 64)
		return err == nil && value&(uint64(1)<<capability) != 0
	}
	return false
}

func fileOwnerNames(info os.FileInfo) (string, string) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "unknown", "unknown"
	}
	owner, ownerErr := user.LookupId(strconv.FormatUint(uint64(stat.Uid), 10))
	group, groupErr := user.LookupGroupId(strconv.FormatUint(uint64(stat.Gid), 10))
	ownerName, groupName := strconv.FormatUint(uint64(stat.Uid), 10), strconv.FormatUint(uint64(stat.Gid), 10)
	if ownerErr == nil {
		ownerName = owner.Username
	}
	if groupErr == nil {
		groupName = group.Name
	}
	return ownerName, groupName
}

func fileHasCapabilities(path string) bool {
	size, err := unix.Getxattr(path, "security.capability", nil)
	return err == nil && size > 0
}

func bridgeConfigurationAllows(configuration, bridgeName string) bool {
	allowed := false
	for _, line := range strings.Split(configuration, "\n") {
		fields := strings.Fields(strings.SplitN(line, "#", 2)[0])
		if len(fields) != 2 || (fields[1] != "all" && fields[1] != bridgeName) {
			continue
		}
		if fields[0] == "deny" {
			return false
		}
		if fields[0] == "allow" {
			allowed = true
		}
	}
	return allowed
}

func snapshotLink(link netlink.Link) (linkSnapshot, error) {
	addresses, err := movableAddresses(link)
	if err != nil {
		return linkSnapshot{}, err
	}
	routes, err := movableRoutes(link)
	if err != nil {
		return linkSnapshot{}, err
	}
	master := ""
	if link.Attrs().MasterIndex != 0 {
		if masterLink, lookupErr := netlink.LinkByIndex(link.Attrs().MasterIndex); lookupErr == nil {
			master = masterLink.Attrs().Name
		}
	}
	return linkSnapshot{
		Name: link.Attrs().Name, Exists: true, IsBridge: link.Type() == "bridge",
		IsUp: link.Attrs().Flags&net.FlagUp != 0, MTU: link.Attrs().MTU,
		HardwareAddress: link.Attrs().HardwareAddr.String(), Master: master,
		Addresses: addresses, Routes: routes,
	}, nil
}

func requireBridge(name string) (netlink.Link, error) {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return nil, fmt.Errorf("find bridge %s: %w", name, err)
	}
	if link.Type() != "bridge" {
		return nil, fmt.Errorf("interface %s is not a Linux bridge", name)
	}
	return link, nil
}

func bridgeMembers(bridge netlink.Link) ([]string, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, err
	}
	var members []string
	for _, link := range links {
		if link.Attrs().MasterIndex == bridge.Attrs().Index {
			members = append(members, link.Attrs().Name)
		}
	}
	sort.Strings(members)
	return members, nil
}

func linkAddressStrings(link netlink.Link) ([]string, error) {
	addresses, err := netlink.AddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(addresses))
	for _, address := range addresses {
		result = append(result, address.IPNet.String())
	}
	sort.Strings(result)
	return result, nil
}

func movableAddresses(link netlink.Link) ([]string, error) {
	addresses, err := netlink.AddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		return nil, err
	}
	var result []string
	for _, address := range addresses {
		if address.IP == nil || !address.IP.IsGlobalUnicast() || address.IP.IsLinkLocalUnicast() {
			continue
		}
		result = append(result, address.IPNet.String())
	}
	sort.Strings(result)
	return result, nil
}

func movableRoutes(link netlink.Link) ([]routeSnapshot, error) {
	routes, err := netlink.RouteList(link, netlink.FAMILY_ALL)
	if err != nil {
		return nil, err
	}
	var result []routeSnapshot
	for _, route := range routes {
		if route.Protocol == unix.RTPROT_KERNEL {
			continue
		}
		if route.Table != 0 && route.Table != unix.RT_TABLE_MAIN {
			continue
		}
		saved := routeSnapshot{Table: route.Table, Priority: route.Priority, Scope: int(route.Scope), Protocol: int(route.Protocol), Type: route.Type}
		if route.Dst != nil {
			saved.Destination = route.Dst.String()
		}
		if route.Gw != nil {
			saved.Gateway = route.Gw.String()
		}
		if route.Src != nil {
			saved.Source = route.Src.String()
		}
		result = append(result, saved)
	}
	return result, nil
}

func moveNetworkConfiguration(source, target netlink.Link) error {
	snapshot, err := snapshotLink(source)
	if err != nil {
		return err
	}
	if err := removeMovableNetworkConfiguration(source); err != nil {
		return err
	}
	return addNetworkConfiguration(target, snapshot.Addresses, snapshot.Routes)
}

func restoreNetworkConfiguration(link netlink.Link, snapshot linkSnapshot) error {
	if err := removeMovableNetworkConfiguration(link); err != nil {
		return err
	}
	return addNetworkConfiguration(link, snapshot.Addresses, snapshot.Routes)
}

func removeMovableNetworkConfiguration(link netlink.Link) error {
	addresses, err := netlink.AddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		return err
	}
	for index := range addresses {
		address := &addresses[index]
		if address.IP == nil || !address.IP.IsGlobalUnicast() || address.IP.IsLinkLocalUnicast() {
			continue
		}
		if err := netlink.AddrDel(link, address); err != nil {
			return err
		}
	}
	routes, err := netlink.RouteList(link, netlink.FAMILY_ALL)
	if err != nil {
		return err
	}
	for index := range routes {
		route := &routes[index]
		if route.Protocol == unix.RTPROT_KERNEL || (route.Table != 0 && route.Table != unix.RT_TABLE_MAIN) {
			continue
		}
		if err := netlink.RouteDel(route); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
	}
	return nil
}

func addNetworkConfiguration(link netlink.Link, addresses []string, routes []routeSnapshot) error {
	for _, cidr := range addresses {
		address, err := netlink.ParseAddr(cidr)
		if err != nil {
			return err
		}
		if err := netlink.AddrAdd(link, address); err != nil && !errors.Is(err, syscall.EEXIST) {
			return err
		}
	}
	for _, saved := range routes {
		route := netlink.Route{LinkIndex: link.Attrs().Index, Table: saved.Table, Priority: saved.Priority, Scope: netlink.Scope(saved.Scope), Protocol: netlink.RouteProtocol(saved.Protocol), Type: saved.Type}
		if saved.Destination != "" {
			_, destination, err := net.ParseCIDR(saved.Destination)
			if err != nil {
				return err
			}
			route.Dst = destination
		}
		if saved.Gateway != "" {
			route.Gw = net.ParseIP(saved.Gateway)
		}
		if saved.Source != "" {
			route.Src = net.ParseIP(saved.Source)
		}
		if err := netlink.RouteAdd(&route); err != nil && !errors.Is(err, syscall.EEXIST) {
			return err
		}
	}
	return nil
}

func isLinkNotFound(err error) bool {
	var notFound netlink.LinkNotFoundError
	return errors.As(err, &notFound)
}
