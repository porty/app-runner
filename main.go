package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
)

func main() {
	workingDirectory, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	settings, err := loadConfig(os.Args[1:], workingDirectory)
	if err != nil {
		fatal(err)
	}
	if err := prepareDirectories(settings); err != nil {
		fatal(err)
	}

	capabilities := func() hostCapabilities { return detectHostCapabilities(settings) }
	hostStatus := capabilities()
	if !hostStatus.QEMUAvailable {
		slog.Warn("QEMU is unavailable", "binary", settings.QEMUBinary)
	}
	if !hostStatus.KVMAvailable {
		slog.Warn("KVM is unavailable to the current user", "device", "/dev/kvm")
	}

	manager, err := newVMManager(settings, newJSONVMStore(settings.DiskDir), newQEMUHypervisor(settings), capabilities)
	if err != nil {
		fatal(err)
	}
	networkProvider := newLinuxNetworkProvider()
	manager.bridgeCapability = networkProvider.BridgeCapability
	dhcp, err := newDHCPManager(settings.DiskDir, networkProvider)
	if err != nil {
		fatal(err)
	}
	defer dhcp.Close()
	manager.networkLifecycle = dhcp
	vms, err := manager.List()
	if err != nil {
		fatal(err)
	}
	if err := dhcp.Reconcile(vms); err != nil {
		slog.Warn("restore bridge DHCP services", "error", err)
	}
	networking, err := newNetworkManager(networkProvider, manager, settings.DiskDir, dhcp)
	if err != nil {
		fatal(err)
	}
	networkStatus, err := networking.Status()
	if err != nil {
		slog.Warn("network diagnostics are unavailable", "error", err)
	} else {
		for _, diagnostic := range networkStatus.Diagnostics {
			if diagnostic.Status == diagnosticFail {
				slog.Warn("network diagnostic failed", "check", diagnostic.Label, "detail", diagnostic.Detail, "remediation", diagnostic.Remediation)
			}
		}
	}
	server := &http.Server{
		Addr:    settings.Listen,
		Handler: newHTTPHandler(productionFrontend(), appRuntime{manager: manager, networking: networking, capabilities: capabilities}),
	}

	slog.Info("App Runner listening", "address", settings.Listen, "iso_dir", settings.ISODir, "disk_dir", settings.DiskDir)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
