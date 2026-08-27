package apprunner

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// RunServer starts the App Runner HTTP and Twirp API server and runs until
// SIGINT or SIGTERM. Shutting down closes the API and the managed DHCP, DNS,
// and IPMI listeners; running virtual machines and the NAT rules serving
// them are deliberately left alone and re-verified on the next start.
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
	nat, err := newNATManager(settings.StateDir)
	if err != nil {
		return err
	}
	dns := newDNSManager()
	dhcp, err := newDHCPManager(settings.StateDir, networkProvider, nat, dns)
	if err != nil {
		return err
	}
	manager, err := newVMManager(vmManagerOptions{
		Settings: settings, Store: newJSONVMStore(settings.StateDir), Hypervisor: newQEMUHypervisor(settings),
		Capabilities: capabilities, BridgeCapability: networkProvider.BridgeCapability, Addresses: dhcp.Addresses,
	})
	if err != nil {
		return err
	}
	templates, err := newCloudInitTemplateManager(newJSONCloudInitTemplateStore(settings.StateDir))
	if err != nil {
		return err
	}
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
		// Console WebSockets and large uploads are long-lived, so only the
		// header read and idle keep-alives are bounded.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	slog.Info("App Runner listening", "address", settings.Listen, "iso_dir", settings.ISODir, "disk_dir", settings.DiskDir, "state_dir", settings.StateDir, "run_dir", settings.RunDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.ListenAndServe() }()
	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}
	slog.Info("shutting down; running virtual machines are left running")
	shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		slog.Warn("HTTP server shutdown", "error", err)
	}
	ipmi.Close()
	if err := dhcp.Shutdown(); err != nil {
		slog.Warn("close managed network listeners", "error", err)
	}
	return nil
}
