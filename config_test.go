package apprunner

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
	if settings.StateDir != filepath.Join(workingDirectory, "state") || settings.RunDir != filepath.Join(workingDirectory, "run") {
		t.Fatalf("unexpected state/run directories: %q %q", settings.StateDir, settings.RunDir)
	}
	if settings.XorrisoBinary != "xorriso" {
		t.Fatalf("unexpected xorriso binary: %q", settings.XorrisoBinary)
	}
	if settings.UEFICodePath == "" || settings.UEFIVarsPath == "" {
		t.Fatalf("UEFI firmware defaults are incomplete: code=%q vars=%q", settings.UEFICodePath, settings.UEFIVarsPath)
	}
}

func TestLoadConfigAppliesFileBeforeCLI(t *testing.T) {
	workingDirectory := t.TempDir()
	configPath := filepath.Join(workingDirectory, defaultConfigName)
	contents := []byte(`{"listen":"127.0.0.1:7000","iso_dir":"images","run_dir":"/var/run/app-runner"}`)
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
	if settings.RunDir != "/var/run/app-runner" {
		t.Fatalf("config file run directory was not applied: %q", settings.RunDir)
	}
}

func TestPrepareDirectoriesCreatesStorage(t *testing.T) {
	workingDirectory := t.TempDir()
	settings := defaultConfig(workingDirectory)

	if err := prepareDirectories(settings); err != nil {
		t.Fatalf("prepareDirectories returned an error: %v", err)
	}
	for _, directory := range []string{settings.ISODir, settings.DiskDir, settings.StateDir, settings.RunDir} {
		info, err := os.Stat(directory)
		if err != nil || !info.IsDir() {
			t.Fatalf("storage directory was not created: %s", directory)
		}
	}
}
