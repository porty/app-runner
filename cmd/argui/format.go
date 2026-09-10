package main

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/twitchtv/twirp"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"
)

var (
	dnsLabelPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?$`)
	macPattern      = regexp.MustCompile(`^(?:[0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}$`)
	cloudImageName  = regexp.MustCompile(`(?i)\.(?:img|qcow2)$`)
	isoName         = regexp.MustCompile(`(?i)\.iso$`)
)

const (
	maxCPUs         = 64
	minMemoryMiB    = 256
	maxMemoryMiB    = 1048576
	maxDiskGiB      = 2048
	maxUserDataSize = 1048576
	maxTemplateName = 80
)

func validDNSLabel(value string) bool {
	return len(value) >= 1 && len(value) <= 63 && dnsLabelPattern.MatchString(value)
}

// validMAC accepts a unicast MAC address, which QEMU requires for a NIC.
func validMAC(value string) bool {
	if !macPattern.MatchString(value) {
		return false
	}
	first, err := strconv.ParseUint(value[:2], 16, 8)
	return err == nil && first&1 == 0
}

func validDNSSuffix(value string) bool {
	suffix := strings.TrimSuffix(strings.TrimSpace(value), ".")
	if suffix == "" || len(suffix) > 253 {
		return false
	}
	for _, label := range strings.Split(suffix, ".") {
		if !validDNSLabel(label) {
			return false
		}
	}
	return true
}

func nameTaken(name string, vms []*apprunnerv1.VirtualMachine, excludeID string) bool {
	for _, vm := range vms {
		if vm.GetId() != excludeID && strings.EqualFold(vm.GetName(), name) {
			return true
		}
	}
	return false
}

func formatBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	value := float64(bytes)
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	index := -1
	for value >= unit && index < len(units)-1 {
		value /= unit
		index++
	}
	if value >= 10 {
		return fmt.Sprintf("%.0f %s", value, units[index])
	}
	return fmt.Sprintf("%.1f %s", value, units[index])
}

func formatMemory(mib uint32) string {
	if mib >= 1024 {
		if mib%1024 == 0 {
			return fmt.Sprintf("%d GiB", mib/1024)
		}
		return fmt.Sprintf("%.1f GiB", float64(mib)/1024)
	}
	return fmt.Sprintf("%d MiB", mib)
}

func formatTime(value string) string {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		if parsed, err = time.Parse(time.RFC3339, value); err != nil {
			return value
		}
	}
	return parsed.Local().Format("2 Jan 2006 15:04")
}

func pluralize(count int, singular, plural string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, singular)
	}
	return fmt.Sprintf("%d %s", count, plural)
}

func vmStatusLabel(status apprunnerv1.VMStatus) string {
	switch status {
	case apprunnerv1.VMStatus_VM_STATUS_STOPPED:
		return "Stopped"
	case apprunnerv1.VMStatus_VM_STATUS_RUNNING:
		return "Running"
	case apprunnerv1.VMStatus_VM_STATUS_STOPPING:
		return "Stopping"
	case apprunnerv1.VMStatus_VM_STATUS_ERROR:
		return "Error"
	default:
		return "Unknown"
	}
}

func vmStatusColor(status apprunnerv1.VMStatus) string {
	switch status {
	case apprunnerv1.VMStatus_VM_STATUS_RUNNING:
		return colorPositive
	case apprunnerv1.VMStatus_VM_STATUS_STOPPING:
		return colorNeutral
	case apprunnerv1.VMStatus_VM_STATUS_ERROR:
		return colorNegative
	default:
		return colorInactive
	}
}

func vmRunning(vm *apprunnerv1.VirtualMachine) bool {
	return vm.GetStatus() == apprunnerv1.VMStatus_VM_STATUS_RUNNING
}

func vmStopping(vm *apprunnerv1.VirtualMachine) bool {
	return vm.GetStatus() == apprunnerv1.VMStatus_VM_STATUS_STOPPING
}

// vmActive reports whether the VM's devices are in use by QEMU.
func vmActive(vm *apprunnerv1.VirtualMachine) bool {
	return vmRunning(vm) || vmStopping(vm)
}

func consoleAvailable(vm *apprunnerv1.VirtualMachine) bool {
	return vmActive(vm)
}

func firmwareLabel(firmware apprunnerv1.VMFirmware) string {
	switch firmware {
	case apprunnerv1.VMFirmware_VM_FIRMWARE_UEFI:
		return "UEFI (OVMF)"
	case apprunnerv1.VMFirmware_VM_FIRMWARE_BIOS:
		return "BIOS (SeaBIOS)"
	default:
		return "Automatic"
	}
}

func nicModeLabel(nic *apprunnerv1.VMNIC) string {
	if nic.GetMode() == apprunnerv1.NetworkMode_NETWORK_MODE_BRIDGE {
		if nic.GetBridgeName() == "" {
			return "Bridge (missing)"
		}
		return "Bridge (" + nic.GetBridgeName() + ")"
	}
	return "NAT"
}

func diskLabel(index int, disk *apprunnerv1.VMDisk) string {
	label := "Disk"
	if index == 0 {
		label = "Boot disk"
	}
	label = fmt.Sprintf("%s %d GiB", label, disk.GetSizeGib())
	if disk.GetSourceImage() != "" {
		label += " · " + disk.GetSourceImage()
	}
	return label
}

func deviceName(index int, kind string) string {
	if kind == "disk" && index == 0 {
		return "Boot disk"
	}
	return fmt.Sprintf("%s %d", map[string]string{"disk": "Disk", "cdrom": "CD-ROM", "nic": "NIC"}[kind], index+1)
}

func vmResources(vm *apprunnerv1.VirtualMachine) string {
	return fmt.Sprintf("%d vCPU · %s · %s", vm.GetCpus(), formatMemory(vm.GetMemoryMib()), pluralize(len(vm.GetDisks()), "disk", "disks"))
}

func vmAddresses(vm *apprunnerv1.VirtualMachine) []string {
	var addresses []string
	for _, nic := range vm.GetNics() {
		addresses = append(addresses, nic.GetAddresses()...)
	}
	return addresses
}

func suggestCloneName(source string, vms []*apprunnerv1.VirtualMachine) string {
	for suffix := 1; suffix < 1000; suffix++ {
		ending := "-copy"
		if suffix > 1 {
			ending = fmt.Sprintf("-copy-%d", suffix)
		}
		base := source
		if len(base) > 63-len(ending) {
			base = base[:63-len(ending)]
		}
		candidate := base + ending
		if !nameTaken(candidate, vms, "") {
			return candidate
		}
	}
	return ""
}

func findBridge(network *apprunnerv1.NetworkingStatus, name string) *apprunnerv1.NetworkBridge {
	for _, bridge := range network.GetBridges() {
		if bridge.GetName() == name {
			return bridge
		}
	}
	return nil
}

// suggestIPMIAddress picks a free host address below the DHCP pool on the
// bridge's /24 network, the same convention the web UI uses.
func suggestIPMIAddress(bridgeName string, network *apprunnerv1.NetworkingStatus, vms []*apprunnerv1.VirtualMachine, excludeID string) string {
	bridge := findBridge(network, bridgeName)
	if bridge == nil {
		return ""
	}
	cidr := ""
	if bridge.GetDhcp().GetEnabled() {
		cidr = bridge.GetDhcp().GetCidr()
	} else {
		for _, address := range bridge.GetAddresses() {
			if strings.Contains(address, ".") {
				cidr = address
				break
			}
		}
	}
	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil || ip.To4() == nil {
		return ""
	}
	if ones, _ := ipNet.Mask.Size(); ones != 24 {
		return ""
	}
	used := map[string]bool{bridge.GetDhcp().GetServerAddress(): true}
	for _, vm := range vms {
		if vm.GetId() != excludeID && vm.GetIpmi().GetEnabled() {
			used[vm.GetIpmi().GetAddress()] = true
		}
	}
	base := ipNet.IP.To4()
	for host := 2; host < 50; host++ {
		candidate := fmt.Sprintf("%d.%d.%d.%d", base[0], base[1], base[2], host)
		if !used[candidate] {
			return candidate
		}
	}
	return ""
}

func firstAvailableStaticAddress(cidr, poolStart string, leases []*apprunnerv1.DHCPLease) string {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil || ipNet.IP.To4() == nil {
		return ""
	}
	pool := net.ParseIP(poolStart).To4()
	if pool == nil {
		return ""
	}
	used := map[string]bool{}
	for _, lease := range leases {
		used[lease.GetAddress()] = true
	}
	network := uint32(ipNet.IP.To4()[0])<<24 | uint32(ipNet.IP.To4()[1])<<16 | uint32(ipNet.IP.To4()[2])<<8 | uint32(ipNet.IP.To4()[3])
	poolValue := uint32(pool[0])<<24 | uint32(pool[1])<<16 | uint32(pool[2])<<8 | uint32(pool[3])
	for candidate := network + 2; candidate < poolValue; candidate++ {
		address := fmt.Sprintf("%d.%d.%d.%d", candidate>>24, candidate>>16&255, candidate>>8&255, candidate&255)
		if !used[address] {
			return address
		}
	}
	return ""
}

func bridgeServices(bridge *apprunnerv1.NetworkBridge) string {
	dhcp := bridge.GetDhcp()
	if !dhcp.GetEnabled() {
		return "no services"
	}
	services := []string{"DHCP"}
	if dhcp.GetNatEnabled() {
		services = append(services, "NAT")
	}
	if dhcp.GetDnsEnabled() {
		services = append(services, "DNS")
	}
	if dhcp.GetHttpBootUrl() != "" {
		services = append(services, "HTTP boot")
	}
	return strings.Join(services, " · ")
}

func workloadNames(bridge *apprunnerv1.NetworkBridge) []string {
	var names []string
	for _, workload := range bridge.GetWorkloads() {
		names = append(names, workload.GetName())
	}
	return names
}

func bridgeMissing(bridge *apprunnerv1.NetworkBridge) bool {
	for _, diagnostic := range bridge.GetDiagnostics() {
		if diagnostic.GetKey() == "bridge_missing" {
			return true
		}
	}
	return false
}

func diagnosticLabel(status apprunnerv1.DiagnosticStatus) (label, color string) {
	switch status {
	case apprunnerv1.DiagnosticStatus_DIAGNOSTIC_STATUS_PASS:
		return "Passed", colorPositive
	case apprunnerv1.DiagnosticStatus_DIAGNOSTIC_STATUS_WARNING:
		return "Warning", colorNeutral
	case apprunnerv1.DiagnosticStatus_DIAGNOSTIC_STATUS_FAIL:
		return "Failed", colorNegative
	case apprunnerv1.DiagnosticStatus_DIAGNOSTIC_STATUS_INFO:
		return "Info", colorHighlight
	default:
		return "Unknown", colorInactive
	}
}

// storageUse records which VM device references a storage file.
type storageUse struct {
	vm    *apprunnerv1.VirtualMachine
	label string
}

// storageUses maps "root:path" to the VM devices that reference the file,
// mirroring the disk naming convention <vm-id>-<disk-id>.qcow2.
func storageUses(vms []*apprunnerv1.VirtualMachine) map[string][]storageUse {
	uses := map[string][]storageUse{}
	add := func(root, path string, vm *apprunnerv1.VirtualMachine, label string) {
		if path == "" {
			return
		}
		key := root + ":" + strings.TrimPrefix(path, "./")
		uses[key] = append(uses[key], storageUse{vm: vm, label: label})
	}
	for _, vm := range vms {
		for index, disk := range vm.GetDisks() {
			label := "Disk"
			if index == 0 {
				label = "Boot disk"
			}
			add("disk", vm.GetId()+"-"+disk.GetId()+".qcow2", vm, label)
			add("iso", disk.GetSourceImage(), vm, "Cloud image")
		}
		for _, cdrom := range vm.GetCdroms() {
			add("iso", cdrom.GetIsoName(), vm, "CD-ROM")
		}
	}
	return uses
}

func errorMessage(err error) string {
	if err == nil {
		return ""
	}
	if twirpError, ok := err.(twirp.Error); ok {
		if twirpError.Msg() != "" {
			return twirpError.Msg()
		}
	}
	return err.Error()
}

func formatCountdown(expiresAt string, now time.Time) (remaining int) {
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return 0
	}
	remaining = int(expires.Sub(now).Seconds() + 0.999)
	return max(remaining, 0)
}
