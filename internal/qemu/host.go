package qemu

import (
	"os"
	"os/exec"

	"github.com/roflware/app-runner/internal/config"
	"github.com/roflware/app-runner/internal/vm"
)

func DetectHostCapabilities(settings config.Config) vm.HostCapabilities {
	status := vm.HostCapabilities{}
	_, qemuErr := exec.LookPath(settings.QEMUBinary)
	status.QEMUAvailable = qemuErr == nil

	kvm, kvmErr := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if kvmErr == nil {
		status.KVMAvailable = true
		_ = kvm.Close()
	}
	return status
}
