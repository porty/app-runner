package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const maxCloudInitUserDataBytes = 1 << 20

type cloudInitISOOptions struct {
	InstanceID string
	Hostname   string
	UserData   string
}

type cloudInitISOBuilder func(context.Context, string, cloudInitISOOptions) error

func createCloudInitISO(ctx context.Context, binary, destination string, options cloudInitISOOptions) error {
	sourceDirectory, err := os.MkdirTemp(filepath.Dir(destination), ".app-runner-cloud-init-source-*")
	if err != nil {
		return fmt.Errorf("create cloud-init source directory: %w", err)
	}
	defer os.RemoveAll(sourceDirectory)

	if err := os.WriteFile(filepath.Join(sourceDirectory, "user-data"), []byte(options.UserData), 0o600); err != nil {
		return fmt.Errorf("write cloud-init user-data: %w", err)
	}
	metadata := fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", options.InstanceID, options.Hostname)
	if err := os.WriteFile(filepath.Join(sourceDirectory, "meta-data"), []byte(metadata), 0o600); err != nil {
		return fmt.Errorf("write cloud-init meta-data: %w", err)
	}

	output, err := os.CreateTemp(filepath.Dir(destination), ".app-runner-cloud-init-*.iso")
	if err != nil {
		return fmt.Errorf("create cloud-init ISO staging file: %w", err)
	}
	outputPath := output.Name()
	if err := output.Close(); err != nil {
		_ = os.Remove(outputPath)
		return err
	}
	if err := os.Remove(outputPath); err != nil {
		return err
	}
	defer os.Remove(outputPath)

	command := exec.CommandContext(ctx, binary,
		"-as", "mkisofs",
		"-output", outputPath,
		"-volid", "cidata",
		"-joliet",
		"-rock",
		"user-data",
		"meta-data",
	)
	command.Dir = sourceDirectory
	commandOutput, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build cloud-init ISO with %s: %s: %w", binary, strings.TrimSpace(string(commandOutput)), err)
	}
	if err := os.Chmod(outputPath, 0o640); err != nil {
		return fmt.Errorf("set cloud-init ISO permissions: %w", err)
	}
	file, err := os.Open(outputPath)
	if err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(outputPath, destination); err != nil {
		return fmt.Errorf("publish cloud-init ISO: %w", err)
	}
	return nil
}
