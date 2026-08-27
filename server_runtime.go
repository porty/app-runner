package apprunner

import (
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
)

// RunServer starts the App Runner HTTP and Twirp API server.
func RunServer(args []string, frontend fs.FS) error {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return err
	}
	settings, err := loadConfig(args, workingDirectory)
	if err != nil {
		return err
	}
	if err := prepareDirectories(settings); err != nil {
		return err
	}

	capabilities := func() hostCapabilities { return detectHostCapabilities(settings) }
	hostStatus := capabilities()
	if !hostStatus.QEMUAvailable {
		slog.Warn("QEMU is unavailable", "binary", settings.QEMUBinary)
	}
	if !hostStatus.KVMAvailable {
		slog.Warn("KVM is unavailable to the current user", "device", "/dev/kvm")
	}

	networkProvider := newLinuxNetworkProvider()
	manager, err := newVMManager(vmManagerOptions{
		Settings: settings, Store: newJSONVMStore(settings.StateDir), Hypervisor: newQEMUHypervisor(settings),
		Capabilities: capabilities, BridgeCapability: networkProvider.BridgeCapability,
	})
	if err != nil {
		return err
	}
	templates, err := newCloudInitTemplateManager(newJSONCloudInitTemplateStore(settings.StateDir))
	if err != nil {
		return err
	}
	nat, err := newNATManager(settings.StateDir)
	if err != nil {
		return err
	}
	dns := newDNSManager()
	dhcp, err := newDHCPManager(settings.StateDir, networkProvider, nat, dns)
	if err != nil {
		return err
	}
	defer dhcp.Close()
	manager.networkLifecycle = dhcp
	ipmi := newIPMIManager(manager, func(bridge string) (netip.Prefix, bool) {
		status := dhcp.Status(bridge)
		if !status.Enabled {
			return netip.Prefix{}, false
		}
		prefix, err := netip.ParsePrefix(status.CIDR)
		return prefix, err == nil
	})
	manager.ipmi = ipmi
	defer ipmi.Close()
	vms, err := manager.List()
	if err != nil {
		return err
	}
	if err := dhcp.Reconcile(vms); err != nil {
		slog.Warn("restore managed bridge network services", "error", err)
	}
	if err := ipmi.Reconcile(vms); err != nil {
		slog.Warn("restore virtual BMC services", "error", err)
	}
	networking, err := newNetworkManager(networkManagerOptions{Provider: networkProvider, VMs: manager, DHCP: dhcp, StateDirectory: settings.StateDir})
	if err != nil {
		return err
	}
	if status, err := networking.Status(); err != nil {
		slog.Warn("network diagnostics are unavailable", "error", err)
	} else {
		for _, diagnostic := range status.Diagnostics {
			if diagnostic.Status == diagnosticFail {
				slog.Warn("network diagnostic failed", "check", diagnostic.Label, "detail", diagnostic.Detail, "remediation", diagnostic.Remediation)
			}
		}
	}

	server := &http.Server{
		Addr: settings.Listen,
		Handler: newHTTPHandler(frontend, appRuntime{
			manager: manager, networking: networking, storage: newStorageService(settings), templates: templates, capabilities: capabilities,
		}),
	}
	slog.Info("App Runner listening", "address", settings.Listen, "iso_dir", settings.ISODir, "disk_dir", settings.DiskDir, "state_dir", settings.StateDir, "run_dir", settings.RunDir)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
