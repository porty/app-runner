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

func (q *qemuHypervisor) Start(vm virtualMachine, onExit func(error)) (int, error) {
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
		onExit(err)
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

func (q *qemuHypervisor) arguments(vm virtualMachine) []string {
	diskPath := filepath.Join(q.settings.DiskDir, vm.ID+".qcow2")
	isoPath := filepath.Join(q.settings.ISODir, vm.ISOName)
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
		"-drive", "file=" + diskPath + ",if=none,id=system,format=qcow2,cache=none",
		"-device", "virtio-blk-pci,drive=system",
		"-device", "virtio-scsi-pci,id=scsi0",
		"-drive", "file=" + isoPath + ",if=none,id=install,media=cdrom,readonly=on",
		"-device", "scsi-cd,drive=install,bus=scsi0.0",
		"-boot", "order=dc,menu=on",
	}
	if vm.NetworkMode == networkModeBridge {
		arguments = append(arguments,
			"-netdev", "bridge,id=net0,br="+q.settings.BridgeName,
			"-device", "virtio-net-pci,netdev=net0",
		)
	} else {
		arguments = append(arguments,
			"-netdev", "user,id=net0",
			"-device", "virtio-net-pci,netdev=net0",
		)
	}
	return append(arguments,
		"-qmp", "unix:"+q.qmpSocketPath(vm.ID)+",server=on,wait=off",
		"-vnc", "unix:"+q.vncSocketPath(vm.ID),
	)
}

func (q *qemuHypervisor) qmpSocketPath(id string) string {
	return filepath.Join(q.settings.DiskDir, id+".qmp.sock")
}

func (q *qemuHypervisor) vncSocketPath(id string) string {
	return filepath.Join(q.settings.DiskDir, id+".vnc.sock")
}

func executeQMP(socketPath, command string) error {
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
	if err := encoder.Encode(map[string]string{"execute": command}); err != nil {
		return err
	}
	if err := waitForQMPResponse(decoder, "return"); err != nil {
		return fmt.Errorf("execute QMP command %s: %w", command, err)
	}
	return nil
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
