package main

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

type config struct {
	Listen        string `json:"listen"`
	ISODir        string `json:"iso_dir"`
	DiskDir       string `json:"disk_dir"`
	BridgeName    string `json:"bridge_name"`
	QEMUBinary    string `json:"qemu_binary"`
	QEMUImgBinary string `json:"qemu_img_binary"`
}

func defaultConfig(workingDirectory string) config {
	return config{
		Listen:        "127.0.0.1:8080",
		ISODir:        filepath.Join(workingDirectory, "iso"),
		DiskDir:       filepath.Join(workingDirectory, "disk"),
		BridgeName:    "br0",
		QEMUBinary:    "qemu-system-x86_64",
		QEMUImgBinary: "qemu-img",
	}
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
	flags.StringVar(&settings.DiskDir, "disk-dir", settings.DiskDir, "directory containing VM data")
	flags.StringVar(&settings.BridgeName, "bridge", settings.BridgeName, "host bridge used by bridge networking")
	flags.StringVar(&settings.QEMUBinary, "qemu", settings.QEMUBinary, "QEMU system binary")
	flags.StringVar(&settings.QEMUImgBinary, "qemu-img", settings.QEMUImgBinary, "qemu-img binary")
	if err := flags.Parse(args); err != nil {
		return config{}, err
	}
	if flags.NArg() != 0 {
		return config{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	settings.ISODir = absoluteFrom(workingDirectory, settings.ISODir)
	settings.DiskDir = absoluteFrom(workingDirectory, settings.DiskDir)
	if strings.TrimSpace(settings.BridgeName) == "" {
		return config{}, errors.New("bridge name cannot be empty")
	}
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
	for _, directory := range []string{settings.ISODir, settings.DiskDir} {
		if err := os.MkdirAll(directory, 0o750); err != nil {
			return fmt.Errorf("create directory %s: %w", directory, err)
		}
	}
	return nil
}
