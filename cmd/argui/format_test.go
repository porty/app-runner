package main

import (
	"testing"
	"time"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"
)

func TestFormatBytes(t *testing.T) {
	cases := map[uint64]string{0: "0 B", 512: "512 B", 641728512: "612 MiB", 4402341478: "4.1 GiB", 150323855360: "140 GiB"}
	for input, want := range cases {
		if got := formatBytes(input); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", input, got, want)
		}
	}
}

func TestFormatMemory(t *testing.T) {
	cases := map[uint32]string{512: "512 MiB", 2048: "2 GiB", 3072: "3 GiB", 1536: "1.5 GiB"}
	for input, want := range cases {
		if got := formatMemory(input); got != want {
			t.Errorf("formatMemory(%d) = %q, want %q", input, got, want)
		}
	}
}

func TestValidation(t *testing.T) {
	if !validDNSLabel("k0-node3") || validDNSLabel("-bad") || validDNSLabel("") || validDNSLabel("a.b") {
		t.Error("DNS label validation is wrong")
	}
	if !validMAC("52:54:00:12:34:56") || validMAC("53:54:00:12:34:56") || validMAC("52:54:00:12:34") {
		t.Error("MAC validation is wrong")
	}
	if !validDNSSuffix("pxe.internal") || !validDNSSuffix("lab.") || validDNSSuffix("") || validDNSSuffix("bad_label.x") {
		t.Error("DNS suffix validation is wrong")
	}
}

func vms(names ...string) []*apprunnerv1.VirtualMachine {
	var result []*apprunnerv1.VirtualMachine
	for index, name := range names {
		result = append(result, &apprunnerv1.VirtualMachine{Id: string(rune('a' + index)), Name: name})
	}
	return result
}

func TestSuggestCloneName(t *testing.T) {
	if got := suggestCloneName("server", vms("server")); got != "server-copy" {
		t.Errorf("first clone name %q", got)
	}
	if got := suggestCloneName("server", vms("server", "server-copy", "Server-Copy-2")); got != "server-copy-3" {
		t.Errorf("clone name skipping taken names %q", got)
	}
	long := "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijk"
	if got := suggestCloneName(long, nil); len(got) != 63 || got[len(got)-5:] != "-copy" {
		t.Errorf("long clone name %q", got)
	}
}

func TestSuggestIPMIAddress(t *testing.T) {
	network := &apprunnerv1.NetworkingStatus{Bridges: []*apprunnerv1.NetworkBridge{{
		Name: "pxe",
		Dhcp: &apprunnerv1.BridgeDHCPStatus{Enabled: true, Cidr: "192.168.102.0/24", ServerAddress: "192.168.102.1"},
	}}}
	existing := []*apprunnerv1.VirtualMachine{{Id: "x", Ipmi: &apprunnerv1.VMIPMIStatus{Enabled: true, Address: "192.168.102.2"}}}
	if got := suggestIPMIAddress("pxe", network, existing, ""); got != "192.168.102.3" {
		t.Errorf("suggested %q", got)
	}
	if got := suggestIPMIAddress("pxe", network, existing, "x"); got != "192.168.102.2" {
		t.Errorf("excluding own VM suggested %q", got)
	}
	if got := suggestIPMIAddress("missing", network, nil, ""); got != "" {
		t.Errorf("unknown bridge suggested %q", got)
	}
	gateway := &apprunnerv1.NetworkingStatus{Bridges: []*apprunnerv1.NetworkBridge{{
		Name: "prov",
		Dhcp: &apprunnerv1.BridgeDHCPStatus{GatewayOnly: true, Cidr: "172.22.0.0/24", ServerAddress: "172.22.0.1"},
	}}}
	if got := suggestIPMIAddress("prov", gateway, nil, ""); got != "172.22.0.2" {
		t.Errorf("gateway-only bridge suggested %q", got)
	}
}

func TestBridgeServicesDescribesGatewayOnlyMode(t *testing.T) {
	bridge := &apprunnerv1.NetworkBridge{Dhcp: &apprunnerv1.BridgeDHCPStatus{GatewayOnly: true, NatEnabled: true, Cidr: "172.22.0.0/24"}}
	if got := bridgeServices(bridge); got != "Gateway only · NAT" {
		t.Errorf("bridgeServices = %q", got)
	}
	if !managedSubnet(bridge) {
		t.Error("a gateway-only bridge was not treated as a managed subnet")
	}
	if got := bridgeServices(&apprunnerv1.NetworkBridge{}); got != "no services" {
		t.Errorf("unconfigured bridgeServices = %q", got)
	}
}

func TestFirstAvailableStaticAddress(t *testing.T) {
	leases := []*apprunnerv1.DHCPLease{{Address: "192.168.100.2"}, {Address: "192.168.100.3"}}
	if got := firstAvailableStaticAddress("192.168.100.0/24", "192.168.100.50", leases); got != "192.168.100.4" {
		t.Errorf("static address %q", got)
	}
	if got := firstAvailableStaticAddress("192.168.100.0/24", "192.168.100.2", nil); got != "" {
		t.Errorf("no room should give empty, got %q", got)
	}
}

func TestStorageUses(t *testing.T) {
	uses := storageUses([]*apprunnerv1.VirtualMachine{{
		Id: "vm1", Name: "server",
		Disks:  []*apprunnerv1.VMDisk{{Id: "d1", SizeGib: 20, SourceImage: "cloud.img"}},
		Cdroms: []*apprunnerv1.VMCDROM{{Id: "c1", IsoName: "installer.iso"}},
	}})
	if got := uses["disk:vm1-d1.qcow2"]; len(got) != 1 || got[0].label != "Boot disk" {
		t.Errorf("disk use %+v", got)
	}
	if got := uses["iso:cloud.img"]; len(got) != 1 || got[0].label != "Cloud image" {
		t.Errorf("cloud image use %+v", got)
	}
	if got := uses["iso:installer.iso"]; len(got) != 1 || got[0].label != "CD-ROM" {
		t.Errorf("cdrom use %+v", got)
	}
}

func TestFormatCountdown(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	if got := formatCountdown(now.Add(11*time.Second+200*time.Millisecond).Format(time.RFC3339Nano), now); got != 12 {
		t.Errorf("countdown %d", got)
	}
	if got := formatCountdown(now.Add(-time.Second).Format(time.RFC3339Nano), now); got != 0 {
		t.Errorf("expired countdown %d", got)
	}
}

func TestValidateUserData(t *testing.T) {
	if verdict := validateUserData("#cloud-config\nusers:\n  - name: rob\n"); !verdict.ok || verdict.message != "Valid cloud-config" {
		t.Errorf("cloud-config verdict %+v", verdict)
	}
	if verdict := validateUserData("#!/bin/sh\necho hi\n"); !verdict.ok {
		t.Errorf("script verdict %+v", verdict)
	}
	if verdict := validateUserData("#cloud-config\nusers: [\n"); verdict.ok {
		t.Errorf("broken YAML verdict %+v", verdict)
	}
	if verdict := validateUserData("packages:\n  - curl\n"); !verdict.ok || verdict.color != colorNeutral {
		t.Errorf("headerless verdict %+v", verdict)
	}
}

func TestRefreshSettings(t *testing.T) {
	if refreshInterval(refreshPaused) != 0 || refreshInterval(refreshCheetah) != 2000 || refreshInterval("bogus") != 10000 {
		t.Error("refresh intervals are wrong")
	}
	if refreshSpeedLabel(refreshTurtle) != "Turtle (60 s)" {
		t.Error("refresh label is wrong")
	}
}
