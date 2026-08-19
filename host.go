package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func detectHostCapabilities(settings config) hostCapabilities {
	status := hostCapabilities{BridgeName: settings.BridgeName}
	_, qemuErr := exec.LookPath(settings.QEMUBinary)
	status.QEMUAvailable = qemuErr == nil

	kvm, kvmErr := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if kvmErr == nil {
		status.KVMAvailable = true
		_ = kvm.Close()
	}

	status.BridgeAvailable, status.BridgeWarning = detectBridgeCapability(settings.BridgeName)
	return status
}

func detectBridgeCapability(bridgeName string) (bool, string) {
	fix := fmt.Sprintf("Create and enable %s, allow it in /etc/qemu/bridge.conf, and ensure the current user or group can access /dev/net/tun and qemu-bridge-helper.", bridgeName)
	if _, err := net.InterfaceByName(bridgeName); err != nil {
		return false, fmt.Sprintf("Bridge %s was not found. %s", bridgeName, fix)
	}
	if _, err := os.Stat(filepath.Join("/sys/class/net", bridgeName, "bridge")); err != nil {
		return false, fmt.Sprintf("Network interface %s is not a Linux bridge. %s", bridgeName, fix)
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		return false, fmt.Sprintf("/dev/net/tun is unavailable. %s", fix)
	}
	if os.Geteuid() == 0 {
		return true, ""
	}

	helperPath := findQEMUBridgeHelper()
	if helperPath == "" {
		return false, "qemu-bridge-helper was not found. " + fix
	}
	configuration, err := os.ReadFile("/etc/qemu/bridge.conf")
	if err != nil || !bridgeConfigurationAllows(string(configuration), bridgeName) {
		return false, fmt.Sprintf("QEMU bridge configuration does not allow %s. %s", bridgeName, fix)
	}
	return true, ""
}

func findQEMUBridgeHelper() string {
	if path, err := exec.LookPath("qemu-bridge-helper"); err == nil {
		return path
	}
	for _, candidate := range []string{"/usr/lib/qemu/qemu-bridge-helper", "/usr/libexec/qemu-bridge-helper"} {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return candidate
		}
	}
	return ""
}

func bridgeConfigurationAllows(configuration, bridgeName string) bool {
	for _, line := range strings.Split(configuration, "\n") {
		fields := strings.Fields(strings.SplitN(line, "#", 2)[0])
		if len(fields) == 2 && fields[0] == "allow" && (fields[1] == "all" || fields[1] == bridgeName) {
			return true
		}
	}
	return false
}
