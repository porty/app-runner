// Package server wires App Runner's subsystems together and runs the API.
package server

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/roflware/app-runner/internal/api"
	"github.com/roflware/app-runner/internal/cloudinit"
	"github.com/roflware/app-runner/internal/config"
	"github.com/roflware/app-runner/internal/dhcp"
	"github.com/roflware/app-runner/internal/dns"
	"github.com/roflware/app-runner/internal/hostnet"
	"github.com/roflware/app-runner/internal/ipmi"
	"github.com/roflware/app-runner/internal/nat"
	"github.com/roflware/app-runner/internal/qemu"
	"github.com/roflware/app-runner/internal/serialconsole"
	"github.com/roflware/app-runner/internal/storage"
	"github.com/roflware/app-runner/internal/vm"
)

// Run starts the App Runner HTTP and Twirp API server and runs until
// SIGINT or SIGTERM. Shutting down closes the API and the managed DHCP, DNS,
// and IPMI listeners; running virtual machines and the NAT rules serving
// them are deliberately left alone and re-verified on the next start.
func Run(args []string, frontend fs.FS) error {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return err
	}
	settings, err := config.Load(args, workingDirectory)
	if err != nil {
		return err
	}
	if err := config.PrepareDirectories(settings); err != nil {
		return err
	}

	capabilities := func() vm.HostCapabilities { return qemu.DetectHostCapabilities(settings) }
	hostStatus := capabilities()
	if !hostStatus.QEMUAvailable {
		slog.Warn("QEMU is unavailable", "binary", settings.QEMUBinary)
	}
	if !hostStatus.KVMAvailable {
		slog.Warn("KVM is unavailable to the current user", "device", "/dev/kvm")
	}

	linuxNetwork := hostnet.NewLinuxProvider()
	natService, err := nat.NewManager(settings.StateDir)
	if err != nil {
		return err
	}
	dnsService := dns.NewManager()
	dhcpService, err := dhcp.NewManager(settings.StateDir, linuxNetwork, natService, dnsService)
	if err != nil {
		return err
	}
	serialConsoles := serialconsole.New(settings.SerialSocketPath)
	defer serialConsoles.Close()
	machines, err := vm.NewManager(vm.ManagerOptions{
		Settings: settings, Store: vm.NewJSONStore(settings.StateDir), Hypervisor: qemu.New(settings, serialConsoles),
		Capabilities: capabilities, BridgeCapability: linuxNetwork.BridgeCapability, Addresses: dhcpService.Addresses,
		NetworkLifecycle: dhcpService,
	})
	if err != nil {
		return err
	}
	templates, err := cloudinit.NewTemplateManager(cloudinit.NewJSONTemplateStore(settings.StateDir))
	if err != nil {
		return err
	}
	bmcService := ipmi.NewManager(machines, dhcpService.Prefix)
	machines.SetIPMIController(bmcService)
	vms, err := machines.List()
	if err != nil {
		return err
	}
	for _, machine := range vms {
		if machine.Status == vm.StatusRunning || machine.Status == vm.StatusStopping {
			if err := serialConsoles.Start(machine.ID); err != nil {
				slog.Warn("restore serial console", "vm", machine.Name, "error", err)
			}
		}
	}
	if err := dhcpService.Reconcile(vms); err != nil {
		slog.Warn("restore managed bridge network services", "error", err)
	}
	if err := bmcService.Reconcile(vms); err != nil {
		slog.Warn("restore virtual BMC services", "error", err)
	}
	hostNetwork, err := hostnet.NewManager(hostnet.ManagerOptions{Provider: linuxNetwork, VMs: machines, DHCP: dhcpService, StateDirectory: settings.StateDir})
	if err != nil {
		return err
	}
	if status, err := hostNetwork.Status(); err != nil {
		slog.Warn("network diagnostics are unavailable", "error", err)
	} else {
		for _, diagnostic := range status.Diagnostics {
			if diagnostic.Status == hostnet.DiagnosticFail {
				slog.Warn("network diagnostic failed", "check", diagnostic.Label, "detail", diagnostic.Detail, "remediation", diagnostic.Remediation)
			}
		}
	}

	server := &http.Server{
		Addr: settings.Listen,
		Handler: api.NewHandler(frontend, api.Runtime{
			VMs: machines, SerialConsoles: serialConsoles, Networking: hostNetwork, Storage: storage.NewService(settings), Templates: templates, Capabilities: capabilities,
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
	bmcService.Close()
	if err := dhcpService.Shutdown(); err != nil {
		slog.Warn("close managed network listeners", "error", err)
	}
	return nil
}
