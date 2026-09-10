// Package qemu launches and controls QEMU/KVM processes for the VM
// manager and speaks QMP to them.
package qemu

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
	"strings"
	"syscall"
	"time"

	"github.com/roflware/app-runner/internal/config"
	"github.com/roflware/app-runner/internal/fsutil"
	"github.com/roflware/app-runner/internal/serialconsole"
	"github.com/roflware/app-runner/internal/vm"
)

type Hypervisor struct {
	settings config.Config
	serial   *serialconsole.Hub
}

func New(settings config.Config, serial *serialconsole.Hub) *Hypervisor {
	return &Hypervisor{settings: settings, serial: serial}
}

func (q *Hypervisor) CreateDisk(ctx context.Context, path string, sizeGiB uint32) error {
	command := exec.CommandContext(ctx, q.settings.QEMUImgBinary, "create", "-f", "qcow2", path, fmt.Sprintf("%dG", sizeGiB))
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", string(output), err)
	}
	return nil
}

func (q *Hypervisor) CreateLinkedDisk(ctx context.Context, path, backingPath string, sizeGiB uint32) error {
	infoCommand := exec.CommandContext(ctx, q.settings.QEMUImgBinary, "info", "--output=json", backingPath)
	output, err := infoCommand.CombinedOutput()
	if err != nil {
		return fmt.Errorf("inspect cloud image with %s: %s: %w", q.settings.QEMUImgBinary, string(output), err)
	}
	var info struct {
		Format      string `json:"format"`
		VirtualSize uint64 `json:"virtual-size"`
	}
	if err := json.Unmarshal(output, &info); err != nil {
		return fmt.Errorf("decode cloud image information: %w", err)
	}
	if info.Format != "qcow2" {
		return fmt.Errorf("cloud image format is %q; qcow2 is required", info.Format)
	}
	requestedSize := uint64(sizeGiB) << 30
	if requestedSize < info.VirtualSize {
		minimumGiB := (info.VirtualSize + (1 << 30) - 1) >> 30
		return fmt.Errorf("requested disk size is smaller than the cloud image; use at least %d GiB", minimumGiB)
	}
	command := exec.CommandContext(ctx, q.settings.QEMUImgBinary,
		"create", "-f", "qcow2", "-F", "qcow2", "-b", backingPath, path, fmt.Sprintf("%dG", sizeGiB),
	)
	output, err = command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("create linked qcow2 disk: %s: %w", string(output), err)
	}
	return nil
}

func (q *Hypervisor) Start(machine vm.VirtualMachine, onExit func(int, error)) (int, error) {
	for _, socket := range []string{q.qmpSocket(machine.ID), q.vncSocket(machine.ID), q.serialSocket(machine.ID)} {
		if err := fsutil.RemoveIfExists(socket); err != nil {
			return 0, fmt.Errorf("remove stale runtime socket: %w", err)
		}
	}

	command := exec.Command(q.settings.QEMUBinary, q.arguments(machine)...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		return 0, err
	}
	if err := q.serial.Start(machine.ID); err != nil {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
		for _, socket := range []string{q.qmpSocket(machine.ID), q.vncSocket(machine.ID), q.serialSocket(machine.ID)} {
			_ = fsutil.RemoveIfExists(socket)
		}
		return 0, fmt.Errorf("connect serial console: %w", err)
	}
	go func() {
		err := command.Wait()
		q.serial.Stop(machine.ID)
		for _, socket := range []string{q.qmpSocket(machine.ID), q.vncSocket(machine.ID), q.serialSocket(machine.ID)} {
			_ = fsutil.RemoveIfExists(socket)
		}
		onExit(command.Process.Pid, err)
	}()
	return command.Process.Pid, nil
}

// IsRunning reports whether the VM's recorded PID is still the QEMU process
// started for it. A live PID alone is not enough: after a host reboot, or
// once the kernel reuses the number, it can belong to an unrelated process,
// so the command line must reference this VM's QMP socket.
func (q *Hypervisor) IsRunning(machine vm.VirtualMachine) bool {
	return processServesQMPSocket(machine.PID, q.qmpSocket(machine.ID))
}

func processServesQMPSocket(pid int, socketPath string) bool {
	if pid <= 0 {
		return false
	}
	contents, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return false
	}
	marker := "unix:" + socketPath
	for _, argument := range strings.Split(string(contents), "\x00") {
		if strings.HasPrefix(argument, marker) {
			return true
		}
	}
	return false
}

func (q *Hypervisor) GracefulStop(machine vm.VirtualMachine) error {
	return executeQMP(q.qmpSocket(machine.ID), "system_powerdown")
}

func (q *Hypervisor) ForceStop(machine vm.VirtualMachine) error {
	if machine.PID <= 0 {
		return nil
	}
	err := syscall.Kill(machine.PID, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (q *Hypervisor) Reset(machine vm.VirtualMachine) error {
	return executeQMP(q.qmpSocket(machine.ID), "system_reset")
}

func (q *Hypervisor) ChangeCDROMMedia(machine vm.VirtualMachine, cdromID, isoPath string) error {
	driveID := q.cdromDriveID(machine, cdromID)
	if driveID == "" {
		return errors.New("CD-ROM device was not found")
	}
	if isoPath == "" {
		return executeQMPCommand(q.qmpSocket(machine.ID), "eject", map[string]any{"id": driveID, "force": true})
	}
	return executeQMPCommand(q.qmpSocket(machine.ID), "blockdev-change-medium", map[string]any{
		"id": driveID, "filename": isoPath, "format": "raw", "read-only-mode": "retain",
	})
}

func (q *Hypervisor) arguments(machine vm.VirtualMachine) []string {
	arguments := []string{
		"-name", machine.Name,
		"-machine", "q35,accel=kvm",
		"-cpu", "host",
		"-smp", strconv.FormatUint(uint64(machine.CPUs), 10),
		"-m", strconv.FormatUint(uint64(machine.MemoryMiB), 10),
		"-nodefaults",
		"-no-user-config",
		"-display", "none",
		"-monitor", "none",
		"-chardev", "socket,id=serial0,path=" + q.serialSocket(machine.ID) + ",server=on,wait=on",
		"-serial", "chardev:serial0",
		"-device", "virtio-vga",
		"-device", "qemu-xhci,id=usb",
		"-device", "usb-kbd,bus=usb.0",
		"-device", "usb-tablet,bus=usb.0",
	}
	if machine.Firmware == vm.FirmwareUEFI {
		arguments = append(arguments,
			"-drive", "if=pflash,format=raw,unit=0,readonly=on,file="+q.settings.UEFICodePath,
			"-drive", "if=pflash,format=raw,unit=1,file="+q.settings.VMUEFIVarsPath(machine.ID),
		)
	}
	for diskIndex, disk := range machine.Disks {
		driveID := "disk" + strconv.Itoa(diskIndex)
		arguments = append(arguments,
			"-drive", "file="+q.settings.VMDiskPath(machine.ID, disk.ID)+",if=none,id="+driveID+",format=qcow2,cache=none",
			"-device", "virtio-blk-pci,drive="+driveID,
		)
	}
	arguments = append(arguments, "-device", "virtio-scsi-pci,id=scsi0")
	for cdromIndex, cdrom := range machine.CDROMs {
		driveID := cdromDriveID(cdromIndex)
		deviceID := cdromDeviceID(cdromIndex)
		if cdrom.ISOName != "" {
			isoPath := q.settings.ISOPath(cdrom.ISOName)
			arguments = append(arguments, "-drive", "file="+isoPath+",if=none,id="+driveID+",media=cdrom,readonly=on")
			arguments = append(arguments, "-device", "scsi-cd,drive="+driveID+",bus=scsi0.0,id="+deviceID)
		} else {
			arguments = append(arguments, "-device", "scsi-cd,id="+deviceID+",bus=scsi0.0")
		}
	}
	if machine.CloudInit.Enabled {
		arguments = append(arguments,
			"-drive", "file="+q.settings.CloudInitISOPath(machine.ID)+",if=none,id=cloudinit,media=cdrom,readonly=on",
			"-device", "scsi-cd,drive=cloudinit,bus=scsi0.0,id=cloudinitdev",
		)
	}
	arguments = append(arguments, "-boot", "order="+q.bootOrder(machine)+",menu=on")
	for nicIndex, nic := range machine.NICs {
		netdevID := "net" + strconv.Itoa(nicIndex)
		if nic.Mode == vm.NetworkModeBridge {
			arguments = append(arguments, "-netdev", "bridge,id="+netdevID+",br="+nic.BridgeName)
		} else {
			arguments = append(arguments, "-netdev", "user,id="+netdevID)
		}
		arguments = append(arguments, "-device", "virtio-net-pci,netdev="+netdevID+",mac="+nic.MACAddress)
	}
	return append(arguments,
		"-qmp", "unix:"+q.qmpSocket(machine.ID)+",server=on,wait=off",
		"-vnc", "unix:"+q.vncSocket(machine.ID),
	)
}

func (q *Hypervisor) bootOrder(machine vm.VirtualMachine) string {
	switch machine.IPMI.BootDevice {
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

func (q *Hypervisor) qmpSocket(id string) string {
	return q.settings.QMPSocketPath(id)
}

func (q *Hypervisor) vncSocket(id string) string {
	return q.settings.VNCSocketPath(id)
}

func (q *Hypervisor) serialSocket(id string) string {
	return q.settings.SerialSocketPath(id)
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

func cdromDriveID(index int) string {
	if index == 0 {
		return "install"
	}
	return "cdrom" + strconv.Itoa(index)
}

func cdromDeviceID(index int) string {
	return "cdromdev" + strconv.Itoa(index)
}

func (q *Hypervisor) cdromDriveID(machine vm.VirtualMachine, id string) string {
	for index, cdrom := range machine.CDROMs {
		if cdrom.ID == id {
			return cdromDeviceID(index)
		}
	}
	return ""
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
