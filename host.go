package apprunner

import (
	"os"
	"os/exec"
	"path/filepath"
)

func detectHostCapabilities(settings config) hostCapabilities {
	status := hostCapabilities{}
	_, qemuErr := exec.LookPath(settings.QEMUBinary)
	status.QEMUAvailable = qemuErr == nil

	kvm, kvmErr := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if kvmErr == nil {
		status.KVMAvailable = true
		_ = kvm.Close()
	}
	return status
}

func locateQEMUBridgeHelper() string {
	if path, err := exec.LookPath("qemu-bridge-helper"); err == nil {
		return path
	}
	for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
		candidate := filepath.Join(directory, "qemu-bridge-helper")
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	for _, candidate := range []string{"/usr/lib/qemu/qemu-bridge-helper", "/usr/libexec/qemu-bridge-helper"} {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	return ""
}
