package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateCloudInitISOWritesNoCloudFilesAndLabel(t *testing.T) {
	binary, err := exec.LookPath("xorriso")
	if err != nil {
		t.Skip("xorriso is not installed")
	}
	directory := t.TempDir()
	destination := filepath.Join(directory, "cloud-init.iso")
	userData := "#cloud-config\npackages:\n  - curl\n"
	if err := createCloudInitISO(context.Background(), binary, destination, cloudInitISOOptions{
		InstanceID: "vm-id", Hostname: "cloud-vm", UserData: userData,
	}); err != nil {
		t.Fatal(err)
	}

	infoOutput, err := exec.Command(binary, "-indev", destination, "-pvd_info").CombinedOutput()
	if err != nil {
		t.Fatalf("inspect cloud-init ISO: %s: %v", infoOutput, err)
	}
	if !strings.Contains(strings.ToLower(string(infoOutput)), "volume id    : 'cidata'") {
		t.Fatalf("cloud-init ISO does not use the cidata label: %s", infoOutput)
	}
	extractedUserData := filepath.Join(directory, "extracted-user-data")
	extractedMetadata := filepath.Join(directory, "extracted-meta-data")
	extractOutput, err := exec.Command(binary,
		"-osirrox", "on", "-indev", destination,
		"-extract", "/user-data", extractedUserData,
		"-extract", "/meta-data", extractedMetadata,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("extract cloud-init ISO: %s: %v", extractOutput, err)
	}
	contents, err := os.ReadFile(extractedUserData)
	if err != nil || string(contents) != userData {
		t.Fatalf("user-data = %q, %v; want %q", contents, err, userData)
	}
	contents, err = os.ReadFile(extractedMetadata)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "instance-id: vm-id\nlocal-hostname: cloud-vm\n" {
		t.Fatalf("unexpected meta-data: %q", contents)
	}
}
