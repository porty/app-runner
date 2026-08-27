package apprunner

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const defaultConfigName = "app-runner.json"

// config describes where App Runner listens and where it keeps its files.
//
// ISODir holds installer ISOs and cloud images, DiskDir holds virtual machine
// disk images, StateDir holds application state (VM definitions, DHCP leases,
// firmware variable stores, generated cloud-init media, recovery records), and
// RunDir holds runtime sockets. Only ISODir and DiskDir are exposed as
// operator-browsable storage.
type config struct {
	Listen        string `json:"listen"`
	ISODir        string `json:"iso_dir"`
	DiskDir       string `json:"disk_dir"`
	StateDir      string `json:"state_dir"`
	RunDir        string `json:"run_dir"`
	QEMUBinary    string `json:"qemu_binary"`
	QEMUImgBinary string `json:"qemu_img_binary"`
	XorrisoBinary string `json:"xorriso_binary"`
	UEFICodePath  string `json:"uefi_code_path"`
	UEFIVarsPath  string `json:"uefi_vars_path"`
}

func defaultConfig(workingDirectory string) config {
	uefiCodePath, uefiVarsPath := defaultUEFIFirmwarePaths()
	return config{
		Listen:        "127.0.0.1:8080",
		ISODir:        filepath.Join(workingDirectory, "iso"),
		DiskDir:       filepath.Join(workingDirectory, "disk"),
		StateDir:      filepath.Join(workingDirectory, "state"),
		RunDir:        filepath.Join(workingDirectory, "run"),
		QEMUBinary:    "qemu-system-x86_64",
		QEMUImgBinary: "qemu-img",
		XorrisoBinary: "xorriso",
		UEFICodePath:  uefiCodePath,
		UEFIVarsPath:  uefiVarsPath,
	}
}

func defaultUEFIFirmwarePaths() (string, string) {
	candidates := [][2]string{
		{"/usr/share/edk2/x64/OVMF_CODE.4m.fd", "/usr/share/edk2/x64/OVMF_VARS.4m.fd"},
		{"/usr/share/OVMF/OVMF_CODE_4M.fd", "/usr/share/OVMF/OVMF_VARS_4M.fd"},
		{"/usr/share/OVMF/OVMF_CODE.fd", "/usr/share/OVMF/OVMF_VARS.fd"},
	}
	for _, candidate := range candidates {
		if regularFile(candidate[0]) && regularFile(candidate[1]) {
			return candidate[0], candidate[1]
		}
	}
	return candidates[0][0], candidates[0][1]
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func loadConfig(args []string, workingDirectory string) (config, error) {
	settings := defaultConfig(workingDirectory)
	configPath, explicitlyConfigured, err := findConfigPath(args, workingDirectory)
	if err != nil {
		return config{}, err
	}

	if err := readConfigFile(configPath, &settings); err != nil {
		if explicitlyConfigured || !errors.Is(err, os.ErrNotExist) {
			return config{}, err
		}
	}

	flags := flag.NewFlagSet("app-runner", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.String("config", configPath, "path to the JSON configuration file")
	flags.StringVar(&settings.Listen, "listen", settings.Listen, "HTTP listen address")
	flags.StringVar(&settings.ISODir, "iso-dir", settings.ISODir, "directory containing ISO images")
	flags.StringVar(&settings.DiskDir, "disk-dir", settings.DiskDir, "directory containing VM disk images")
	flags.StringVar(&settings.StateDir, "state-dir", settings.StateDir, "directory containing application state")
	flags.StringVar(&settings.RunDir, "run-dir", settings.RunDir, "directory containing runtime sockets")
	flags.StringVar(&settings.QEMUBinary, "qemu", settings.QEMUBinary, "QEMU system binary")
	flags.StringVar(&settings.QEMUImgBinary, "qemu-img", settings.QEMUImgBinary, "qemu-img binary")
	flags.StringVar(&settings.XorrisoBinary, "xorriso", settings.XorrisoBinary, "xorriso binary used to build cloud-init ISO images")
	flags.StringVar(&settings.UEFICodePath, "uefi-code", settings.UEFICodePath, "read-only OVMF UEFI code image")
	flags.StringVar(&settings.UEFIVarsPath, "uefi-vars", settings.UEFIVarsPath, "OVMF UEFI variable store template")
	if err := flags.Parse(args); err != nil {
		return config{}, err
	}
	if flags.NArg() != 0 {
		return config{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	settings.ISODir = absoluteFrom(workingDirectory, settings.ISODir)
	settings.DiskDir = absoluteFrom(workingDirectory, settings.DiskDir)
	settings.StateDir = absoluteFrom(workingDirectory, settings.StateDir)
	settings.RunDir = absoluteFrom(workingDirectory, settings.RunDir)
	settings.UEFICodePath = absoluteFrom(workingDirectory, settings.UEFICodePath)
	settings.UEFIVarsPath = absoluteFrom(workingDirectory, settings.UEFIVarsPath)
	return settings, nil
}

func findConfigPath(args []string, workingDirectory string) (string, bool, error) {
	path := filepath.Join(workingDirectory, defaultConfigName)
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "-config" || argument == "--config" {
			if index+1 >= len(args) {
				return "", true, errors.New("-config requires a path")
			}
			return absoluteFrom(workingDirectory, args[index+1]), true, nil
		}
		if value, found := strings.CutPrefix(argument, "-config="); found {
			return absoluteFrom(workingDirectory, value), true, nil
		}
		if value, found := strings.CutPrefix(argument, "--config="); found {
			return absoluteFrom(workingDirectory, value), true, nil
		}
	}
	return path, false, nil
}

func readConfigFile(path string, target *config) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode config %s: %w", path, err)
	}
	return nil
}

func absoluteFrom(workingDirectory, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(workingDirectory, path)
}

func prepareDirectories(settings config) error {
	for _, directory := range []string{settings.ISODir, settings.DiskDir, settings.StateDir, settings.RunDir} {
		if err := os.MkdirAll(directory, 0o750); err != nil {
			return fmt.Errorf("create directory %s: %w", directory, err)
		}
	}
	return nil
}

// vmStateDir is the per-VM directory for firmware variables and generated media.
func (c config) vmStateDir(id string) string {
	return filepath.Join(c.StateDir, "vms", id)
}

// vmDiskPath is the qcow2 image for one of a VM's disks.
func (c config) vmDiskPath(vmID, diskID string) string {
	return filepath.Join(c.DiskDir, vmID+"-"+diskID+".qcow2")
}

func (c config) uefiVarsPath(id string) string {
	return filepath.Join(c.vmStateDir(id), "uefi-vars.fd")
}

func (c config) cloudInitISOPath(id string) string {
	return filepath.Join(c.vmStateDir(id), "cloud-init.iso")
}

func (c config) qmpSocketPath(id string) string {
	return filepath.Join(c.RunDir, id+".qmp.sock")
}

func (c config) vncSocketPath(id string) string {
	return filepath.Join(c.RunDir, id+".vnc.sock")
}

func (c config) isoPath(name string) string {
	return filepath.Join(c.ISODir, name)
}
