package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

type qemuHypervisor struct {
	settings config
}

func newQEMUHypervisor(settings config) *qemuHypervisor {
	return &qemuHypervisor{settings: settings}
}

func (q *qemuHypervisor) CreateDisk(ctx context.Context, path string, sizeGiB uint32) error {
	command := exec.CommandContext(ctx, q.settings.QEMUImgBinary, "create", "-f", "qcow2", path, fmt.Sprintf("%dG", sizeGiB))
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", string(output), err)
	}
	return nil
}

func (q *qemuHypervisor) Start(vm virtualMachine, onExit func(int, error)) (int, error) {
	for _, socket := range []string{q.qmpSocketPath(vm.ID), q.vncSocketPath(vm.ID)} {
		if err := removeIfExists(socket); err != nil {
			return 0, fmt.Errorf("remove stale runtime socket: %w", err)
		}
	}

	command := exec.Command(q.settings.QEMUBinary, q.arguments(vm)...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		return 0, err
	}
	go func() {
		err := command.Wait()
		for _, socket := range []string{q.qmpSocketPath(vm.ID), q.vncSocketPath(vm.ID)} {
			_ = removeIfExists(socket)
		}
		onExit(command.Process.Pid, err)
	}()
	return command.Process.Pid, nil
}

func (q *qemuHypervisor) IsRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func (q *qemuHypervisor) GracefulStop(vm virtualMachine) error {
	return executeQMP(q.qmpSocketPath(vm.ID), "system_powerdown")
}

func (q *qemuHypervisor) ForceStop(vm virtualMachine) error {
	if vm.PID <= 0 {
		return nil
	}
	err := syscall.Kill(vm.PID, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (q *qemuHypervisor) Reset(vm virtualMachine) error {
	return executeQMP(q.qmpSocketPath(vm.ID), "system_reset")
}

func (q *qemuHypervisor) ChangeCDROMMedia(vm virtualMachine, cdromID, isoPath string) error {
	driveID := q.cdromDriveID(vm, cdromID)
	if driveID == "" {
		return errors.New("CD-ROM device was not found")
	}
	if isoPath == "" {
		return executeQMPCommand(q.qmpSocketPath(vm.ID), "eject", map[string]any{"id": driveID, "force": true})
	}
	return executeQMPCommand(q.qmpSocketPath(vm.ID), "blockdev-change-medium", map[string]any{
		"id": driveID, "filename": isoPath, "format": "raw", "read-only-mode": "retain",
	})
}

func (q *qemuHypervisor) arguments(vm virtualMachine) []string {
	arguments := []string{
		"-name", vm.Name,
		"-machine", "q35,accel=kvm",
		"-cpu", "host",
		"-smp", strconv.FormatUint(uint64(vm.CPUs), 10),
		"-m", strconv.FormatUint(uint64(vm.MemoryMiB), 10),
		"-nodefaults",
		"-no-user-config",
		"-display", "none",
		"-monitor", "none",
		"-serial", "none",
		"-device", "virtio-vga",
		"-device", "qemu-xhci,id=usb",
		"-device", "usb-kbd,bus=usb.0",
		"-device", "usb-tablet,bus=usb.0",
	}
	for diskIndex, disk := range effectiveVMDisks(vm) {
		driveID := "disk" + strconv.Itoa(diskIndex)
		if disk.System {
			driveID = "system"
		}
		diskPath := q.vmDiskPath(vm.ID, disk)
		arguments = append(arguments,
			"-drive", "file="+diskPath+",if=none,id="+driveID+",format=qcow2,cache=none",
			"-device", "virtio-blk-pci,drive="+driveID,
		)
	}
	arguments = append(arguments, "-device", "virtio-scsi-pci,id=scsi0")
	for cdromIndex, cdrom := range effectiveVMCDROMs(vm) {
		driveID := cdromDriveID(cdromIndex)
		deviceID := cdromDeviceID(cdromIndex)
		if cdrom.ISOName != "" {
			isoPath := filepath.Join(q.settings.ISODir, cdrom.ISOName)
			arguments = append(arguments, "-drive", "file="+isoPath+",if=none,id="+driveID+",media=cdrom,readonly=on")
			arguments = append(arguments, "-device", "scsi-cd,drive="+driveID+",bus=scsi0.0,id="+deviceID)
		} else {
			arguments = append(arguments, "-device", "scsi-cd,id="+deviceID+",bus=scsi0.0")
		}
	}
	arguments = append(arguments, "-boot", "order="+q.bootOrder(vm)+",menu=on")
	if vm.NetworkMode == networkModeBridge {
		networkDevice := "virtio-net-pci,netdev=net0"
		if vm.MACAddress != "" {
			networkDevice += ",mac=" + vm.MACAddress
		}
		arguments = append(arguments,
			"-netdev", "bridge,id=net0,br="+vm.BridgeName,
			"-device", networkDevice,
		)
	} else {
		networkDevice := "virtio-net-pci,netdev=net0"
		if vm.MACAddress != "" {
			networkDevice += ",mac=" + vm.MACAddress
		}
		arguments = append(arguments,
			"-netdev", "user,id=net0",
			"-device", networkDevice,
		)
	}
	return append(arguments,
		"-qmp", "unix:"+q.qmpSocketPath(vm.ID)+",server=on,wait=off",
		"-vnc", "unix:"+q.vncSocketPath(vm.ID),
	)
}

func (q *qemuHypervisor) bootOrder(vm virtualMachine) string {
	switch vm.IPMI.BootDevice {
	case 1:
		return "ncd"
	case 2:
		return "cdn"
	case 5:
		return "dcn"
	default:
		return "dcn"
	}
}

func (q *qemuHypervisor) qmpSocketPath(id string) string {
	return filepath.Join(q.settings.DiskDir, id+".qmp.sock")
}

func (q *qemuHypervisor) vncSocketPath(id string) string {
	return filepath.Join(q.settings.DiskDir, id+".vnc.sock")
}

func executeQMP(socketPath, command string) error {
	return executeQMPCommand(socketPath, command, nil)
}

func executeQMPCommand(socketPath, command string, arguments map[string]any) error {
	connection, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return fmt.Errorf("connect to QMP: %w", err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}

	decoder := json.NewDecoder(bufio.NewReader(connection))
	encoder := json.NewEncoder(connection)
	if err := waitForQMPResponse(decoder, "QMP"); err != nil {
		return fmt.Errorf("read QMP greeting: %w", err)
	}
	if err := encoder.Encode(map[string]string{"execute": "qmp_capabilities"}); err != nil {
		return err
	}
	if err := waitForQMPResponse(decoder, "return"); err != nil {
		return fmt.Errorf("enable QMP capabilities: %w", err)
	}
	request := map[string]any{"execute": command}
	if arguments != nil {
		request["arguments"] = arguments
	}
	if err := encoder.Encode(request); err != nil {
		return err
	}
	if err := waitForQMPResponse(decoder, "return"); err != nil {
		return fmt.Errorf("execute QMP command %s: %w", command, err)
	}
	return nil
}

func effectiveVMDisks(vm virtualMachine) []vmDisk {
	if len(vm.Disks) != 0 {
		return vm.Disks
	}
	return []vmDisk{{ID: "system", SizeGiB: vm.DiskGiB, System: true}}
}

func effectiveVMCDROMs(vm virtualMachine) []vmCDROM {
	if len(vm.CDROMs) != 0 {
		return vm.CDROMs
	}
	if vm.ISOName == "" {
		return nil
	}
	return []vmCDROM{{ID: "cdrom0", ISOName: vm.ISOName}}
}

func cdromDriveID(index int) string {
	if index == 0 {
		return "install"
	}
	return "cdrom" + strconv.Itoa(index)
}

func cdromDeviceID(index int) string {
	return "cdromdev" + strconv.Itoa(index)
}

func (q *qemuHypervisor) cdromDriveID(vm virtualMachine, id string) string {
	for index, cdrom := range effectiveVMCDROMs(vm) {
		if cdrom.ID == id {
			return cdromDeviceID(index)
		}
	}
	return ""
}

func (q *qemuHypervisor) vmDiskPath(vmID string, disk vmDisk) string {
	if disk.System {
		return filepath.Join(q.settings.DiskDir, vmID+".qcow2")
	}
	return filepath.Join(q.settings.DiskDir, vmID+"-"+disk.ID+".qcow2")
}

func waitForQMPResponse(decoder *json.Decoder, expectedKey string) error {
	for {
		var response map[string]json.RawMessage
		if err := decoder.Decode(&response); err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("QMP connection closed")
			}
			return err
		}
		if qmpError, found := response["error"]; found {
			return fmt.Errorf("QMP error: %s", qmpError)
		}
		if _, found := response[expectedKey]; found {
			return nil
		}
	}
}
