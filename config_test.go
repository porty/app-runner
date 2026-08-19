package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigUsesDefaultsAndCLIOverrides(t *testing.T) {
	workingDirectory := t.TempDir()
	settings, err := loadConfig([]string{"-listen", "127.0.0.1:9000"}, workingDirectory)
	if err != nil {
		t.Fatalf("loadConfig returned an error: %v", err)
	}

	if settings.Listen != "127.0.0.1:9000" {
		t.Fatalf("unexpected listen address: %q", settings.Listen)
	}
	if settings.ISODir != filepath.Join(workingDirectory, "iso") {
		t.Fatalf("unexpected ISO directory: %q", settings.ISODir)
	}
	if settings.DiskDir != filepath.Join(workingDirectory, "disk") {
		t.Fatalf("unexpected disk directory: %q", settings.DiskDir)
	}
}

func TestLoadConfigAppliesFileBeforeCLI(t *testing.T) {
	workingDirectory := t.TempDir()
	configPath := filepath.Join(workingDirectory, defaultConfigName)
	contents := []byte(`{"listen":"127.0.0.1:7000","iso_dir":"images","bridge_name":"lab0"}`)
	if err := os.WriteFile(configPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	settings, err := loadConfig([]string{"-listen", "127.0.0.1:8000"}, workingDirectory)
	if err != nil {
		t.Fatalf("loadConfig returned an error: %v", err)
	}
	if settings.Listen != "127.0.0.1:8000" {
		t.Fatalf("CLI did not override config file: %q", settings.Listen)
	}
	if settings.ISODir != filepath.Join(workingDirectory, "images") {
		t.Fatalf("config file ISO directory was not resolved: %q", settings.ISODir)
	}
	if settings.BridgeName != "lab0" {
		t.Fatalf("unexpected bridge name: %q", settings.BridgeName)
	}
}

func TestPrepareDirectoriesCreatesStorage(t *testing.T) {
	workingDirectory := t.TempDir()
	settings := defaultConfig(workingDirectory)

	if err := prepareDirectories(settings); err != nil {
		t.Fatalf("prepareDirectories returned an error: %v", err)
	}
	for _, directory := range []string{settings.ISODir, settings.DiskDir} {
		info, err := os.Stat(directory)
		if err != nil || !info.IsDir() {
			t.Fatalf("storage directory was not created: %s", directory)
		}
	}
}
