package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"
	"github.com/twitchtv/twirp"
)

type appRunnerService struct {
	manager      *vmManager
	networking   *networkManager
	capabilities func() hostCapabilities
	now          func() time.Time
}

func newAppRunnerService(manager *vmManager, capabilities func() hostCapabilities, networking ...*networkManager) *appRunnerService {
	service := &appRunnerService{manager: manager, capabilities: capabilities, now: time.Now}
	if len(networking) != 0 {
		service.networking = networking[0]
	}
	return service
}

func (s *appRunnerService) Ping(context.Context, *apprunnerv1.PingRequest) (*apprunnerv1.PingResponse, error) {
	return &apprunnerv1.PingResponse{
		Message:    "App Runner backend is ready",
		ServerTime: s.now().UTC().Format(time.RFC3339),
	}, nil
}

func (s *appRunnerService) Echo(_ context.Context, request *apprunnerv1.EchoRequest) (*apprunnerv1.EchoResponse, error) {
	message := strings.TrimSpace(request.GetMessage())
	if message == "" {
		return nil, twirp.InvalidArgumentError("message", "a message is required")
	}
	return &apprunnerv1.EchoResponse{Message: message}, nil
}

func (s *appRunnerService) GetHostStatus(context.Context, *apprunnerv1.GetHostStatusRequest) (*apprunnerv1.GetHostStatusResponse, error) {
	if s.capabilities == nil {
		return nil, twirp.InternalError("host capabilities are not configured")
	}
	status := s.capabilities()
	return &apprunnerv1.GetHostStatusResponse{Status: &apprunnerv1.HostStatus{
		QemuAvailable:   status.QEMUAvailable,
		KvmAvailable:    status.KVMAvailable,
		BridgeAvailable: status.BridgeAvailable,
		BridgeName:      status.BridgeName,
		BridgeWarning:   status.BridgeWarning,
	}}, nil
}

func (s *appRunnerService) ListISOs(context.Context, *apprunnerv1.ListISOsRequest) (*apprunnerv1.ListISOsResponse, error) {
	if err := s.requireManager(); err != nil {
		return nil, err
	}
	images, err := s.manager.ListISOs()
	if err != nil {
		return nil, rpcError(err)
	}
	response := &apprunnerv1.ListISOsResponse{Images: make([]*apprunnerv1.ISOImage, 0, len(images))}
	for _, image := range images {
		response.Images = append(response.Images, &apprunnerv1.ISOImage{Name: image.Name, SizeBytes: image.SizeBytes})
	}
	return response, nil
}

func (s *appRunnerService) ListVMs(context.Context, *apprunnerv1.ListVMsRequest) (*apprunnerv1.ListVMsResponse, error) {
	if err := s.requireManager(); err != nil {
		return nil, err
	}
	vms, err := s.manager.List()
	if err != nil {
		return nil, rpcError(err)
	}
	response := &apprunnerv1.ListVMsResponse{VirtualMachines: make([]*apprunnerv1.VirtualMachine, 0, len(vms))}
	for _, vm := range vms {
		response.VirtualMachines = append(response.VirtualMachines, virtualMachineToProto(vm))
	}
	return response, nil
}

func (s *appRunnerService) GetVM(_ context.Context, request *apprunnerv1.GetVMRequest) (*apprunnerv1.GetVMResponse, error) {
	if err := s.requireManager(); err != nil {
		return nil, err
	}
	vm, err := s.manager.Get(request.GetId())
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.GetVMResponse{VirtualMachine: virtualMachineToProto(vm)}, nil
}

func (s *appRunnerService) CreateVM(ctx context.Context, request *apprunnerv1.CreateVMRequest) (*apprunnerv1.CreateVMResponse, error) {
	if err := s.requireManager(); err != nil {
		return nil, err
	}
	mode, err := networkModeFromProto(request.GetNetworkMode())
	if err != nil {
		return nil, err
	}
	vm, err := s.manager.Create(ctx, createVMOptions{
		Name: request.GetName(), CPUs: request.GetCpus(), MemoryMiB: request.GetMemoryMib(),
		DiskGiB: request.GetDiskGib(), ISOName: request.GetIsoName(), NetworkMode: mode,
		BridgeName: request.GetBridgeName(),
	})
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.CreateVMResponse{VirtualMachine: virtualMachineToProto(vm)}, nil
}

func (s *appRunnerService) StartVM(_ context.Context, request *apprunnerv1.StartVMRequest) (*apprunnerv1.StartVMResponse, error) {
	if err := s.requireManager(); err != nil {
		return nil, err
	}
	vm, err := s.manager.Start(request.GetId())
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.StartVMResponse{VirtualMachine: virtualMachineToProto(vm)}, nil
}

func (s *appRunnerService) StopVM(_ context.Context, request *apprunnerv1.StopVMRequest) (*apprunnerv1.StopVMResponse, error) {
	if err := s.requireManager(); err != nil {
		return nil, err
	}
	vm, err := s.manager.Stop(request.GetId(), request.GetForce())
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.StopVMResponse{VirtualMachine: virtualMachineToProto(vm)}, nil
}

func (s *appRunnerService) DeleteVM(_ context.Context, request *apprunnerv1.DeleteVMRequest) (*apprunnerv1.DeleteVMResponse, error) {
	if err := s.requireManager(); err != nil {
		return nil, err
	}
	if err := s.manager.Delete(request.GetId()); err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.DeleteVMResponse{Id: request.GetId()}, nil
}

func (s *appRunnerService) GetNetworkingStatus(context.Context, *apprunnerv1.GetNetworkingStatusRequest) (*apprunnerv1.GetNetworkingStatusResponse, error) {
	if s.networking == nil {
		return nil, twirp.InternalError("network manager is not configured")
	}
	status, err := s.networking.Status()
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.GetNetworkingStatusResponse{Status: networkingStatusToProto(status)}, nil
}

func (s *appRunnerService) ApplyNetworkChange(ctx context.Context, request *apprunnerv1.ApplyNetworkChangeRequest) (*apprunnerv1.ApplyNetworkChangeResponse, error) {
	if s.networking == nil {
		return nil, twirp.InternalError("network manager is not configured")
	}
	if !isLoopbackRequest(ctx) {
		return nil, twirp.NewError(twirp.PermissionDenied, "network changes are accepted only from a browser connected through loopback")
	}
	changeType, err := networkChangeTypeFromProto(request.GetType())
	if err != nil {
		return nil, err
	}
	change := networkChange{
		Type: changeType, BridgeName: request.GetBridgeName(), InterfaceName: request.GetInterfaceName(),
		MigrateAddresses: request.GetMigrateAddresses(),
	}
	if err := validateNetworkChange(change); err != nil {
		return nil, twirp.InvalidArgumentError("network_change", err.Error())
	}
	pending, err := s.networking.Apply(ctx, change)
	if err != nil {
		return nil, twirp.NewError(twirp.FailedPrecondition, err.Error())
	}
	return &apprunnerv1.ApplyNetworkChangeResponse{PendingChange: pendingNetworkChangeToProto(&pending)}, nil
}

func (s *appRunnerService) ConfirmNetworkChange(ctx context.Context, request *apprunnerv1.ConfirmNetworkChangeRequest) (*apprunnerv1.ConfirmNetworkChangeResponse, error) {
	if s.networking == nil {
		return nil, twirp.InternalError("network manager is not configured")
	}
	if !isLoopbackRequest(ctx) {
		return nil, twirp.NewError(twirp.PermissionDenied, "network changes are accepted only from a browser connected through loopback")
	}
	if err := s.networking.Confirm(request.GetId()); err != nil {
		return nil, twirp.NotFoundError(err.Error())
	}
	status, err := s.networking.Status()
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.ConfirmNetworkChangeResponse{Status: networkingStatusToProto(status)}, nil
}

func (s *appRunnerService) RevertNetworkChange(ctx context.Context, request *apprunnerv1.RevertNetworkChangeRequest) (*apprunnerv1.RevertNetworkChangeResponse, error) {
	if s.networking == nil {
		return nil, twirp.InternalError("network manager is not configured")
	}
	if !isLoopbackRequest(ctx) {
		return nil, twirp.NewError(twirp.PermissionDenied, "network changes are accepted only from a browser connected through loopback")
	}
	if err := s.networking.Revert(request.GetId()); err != nil {
		return nil, twirp.NewError(twirp.FailedPrecondition, err.Error())
	}
	status, err := s.networking.Status()
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.RevertNetworkChangeResponse{Status: networkingStatusToProto(status)}, nil
}

func (s *appRunnerService) ConfigureBridgeDHCP(ctx context.Context, request *apprunnerv1.ConfigureBridgeDHCPRequest) (*apprunnerv1.ConfigureBridgeDHCPResponse, error) {
	if s.networking == nil {
		return nil, twirp.InternalError("network manager is not configured")
	}
	if !isLoopbackRequest(ctx) {
		return nil, twirp.NewError(twirp.PermissionDenied, "managed network service changes are accepted only from a browser connected through loopback")
	}
	if err := s.networking.ConfigureBridgeDHCP(
		request.GetBridgeName(), request.GetEnabled(), request.GetCidr(), request.GetNatEnabled(),
		bridgeDNSConfig{
			Enabled: request.GetDnsEnabled(), Forwarders: request.GetDnsForwarders(),
			Auto: request.GetAutoDns(), Suffix: request.GetDnsSuffix(),
		},
	); err != nil {
		return nil, twirp.NewError(twirp.FailedPrecondition, err.Error())
	}
	status, err := s.networking.Status()
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.ConfigureBridgeDHCPResponse{Status: networkingStatusToProto(status)}, nil
}

func (s *appRunnerService) requireManager() error {
	if s.manager == nil {
		return twirp.InternalError("virtual machine manager is not configured")
	}
	return nil
}

func virtualMachineToProto(vm virtualMachine) *apprunnerv1.VirtualMachine {
	status := apprunnerv1.VMStatus_VM_STATUS_UNSPECIFIED
	switch vm.Status {
	case vmStatusStopped:
		status = apprunnerv1.VMStatus_VM_STATUS_STOPPED
	case vmStatusRunning:
		status = apprunnerv1.VMStatus_VM_STATUS_RUNNING
	case vmStatusStopping:
		status = apprunnerv1.VMStatus_VM_STATUS_STOPPING
	case vmStatusError:
		status = apprunnerv1.VMStatus_VM_STATUS_ERROR
	}
	mode := apprunnerv1.NetworkMode_NETWORK_MODE_NAT
	if vm.NetworkMode == networkModeBridge {
		mode = apprunnerv1.NetworkMode_NETWORK_MODE_BRIDGE
	}
	return &apprunnerv1.VirtualMachine{
		Id: vm.ID, Name: vm.Name, Cpus: vm.CPUs, MemoryMib: vm.MemoryMiB, DiskGib: vm.DiskGiB,
		IsoName: vm.ISOName, NetworkMode: mode, Status: status, BridgeName: vm.BridgeName,
		CreatedAt: vm.CreatedAt.Format(time.RFC3339), LastError: vm.LastError,
		ConsolePath: "/console/" + vm.ID,
	}
}

func networkModeFromProto(mode apprunnerv1.NetworkMode) (networkMode, error) {
	switch mode {
	case apprunnerv1.NetworkMode_NETWORK_MODE_NAT:
		return networkModeNAT, nil
	case apprunnerv1.NetworkMode_NETWORK_MODE_BRIDGE:
		return networkModeBridge, nil
	default:
		return "", twirp.InvalidArgumentError("network_mode", "must be NAT or bridge")
	}
}

func rpcError(err error) error {
	var invalidField *fieldError
	switch {
	case errors.As(err, &invalidField):
		return twirp.InvalidArgumentError(invalidField.field, invalidField.message)
	case errors.Is(err, errVMNotFound):
		return twirp.NotFoundError(err.Error())
	case errors.Is(err, errVMNameExists):
		return twirp.NewError(twirp.AlreadyExists, err.Error())
	case errors.Is(err, errVMAlreadyRunning), errors.Is(err, errVMNotRunning), errors.Is(err, errBridgeUnavailable),
		errors.Is(err, errHostUnavailable), errors.Is(err, errDeleteRunningVM):
		return twirp.NewError(twirp.FailedPrecondition, err.Error())
	default:
		return twirp.InternalError(fmt.Sprintf("virtual machine operation failed: %v", err))
	}
}

func networkingStatusToProto(status networkingStatus) *apprunnerv1.NetworkingStatus {
	response := &apprunnerv1.NetworkingStatus{
		User: &apprunnerv1.UserIdentity{
			Username: status.User.Username, Uid: status.User.UID, Groups: status.User.Groups,
			IsRoot: status.User.IsRoot, HasCapNetAdmin: status.User.HasCAPNetAdmin,
			HasCapNetBindService: status.User.HasCAPNetBindService, HasCapNetRaw: status.User.HasCAPNetRaw,
		},
		CanManage:     status.CanManage,
		PendingChange: pendingNetworkChangeToProto(status.Pending),
	}
	for _, diagnostic := range status.Diagnostics {
		response.Diagnostics = append(response.Diagnostics, networkDiagnosticToProto(diagnostic))
	}
	for _, networkInterface := range status.Interfaces {
		response.Interfaces = append(response.Interfaces, &apprunnerv1.NetworkInterface{
			Name: networkInterface.Name, IsUp: networkInterface.IsUp, Mtu: networkInterface.MTU,
			HardwareAddress: networkInterface.HardwareAddress, Addresses: networkInterface.Addresses,
			Master: networkInterface.Master, IsBridge: networkInterface.IsBridge, CanAttach: networkInterface.CanAttach,
		})
	}
	for _, bridge := range status.Bridges {
		mapped := &apprunnerv1.NetworkBridge{
			Name: bridge.Name, IsUp: bridge.IsUp, Mtu: bridge.MTU,
			HardwareAddress: bridge.HardwareAddress, Addresses: bridge.Addresses,
			MemberInterfaces: bridge.MemberInterfaces, UsableByQemu: bridge.UsableByQEMU,
			Dhcp: &apprunnerv1.BridgeDHCPStatus{
				Enabled: bridge.DHCP.Enabled, Cidr: bridge.DHCP.CIDR, ServerAddress: bridge.DHCP.ServerAddress,
				PoolStart: bridge.DHCP.PoolStart, PoolEnd: bridge.DHCP.PoolEnd, Running: bridge.DHCP.Running,
				ActiveLeases: bridge.DHCP.ActiveLeases, LastError: bridge.DHCP.LastError,
				NatEnabled: bridge.DHCP.NATEnabled, NatRunning: bridge.DHCP.NATRunning,
				DnsEnabled: bridge.DHCP.DNSEnabled, DnsForwarders: bridge.DHCP.DNSForwarders,
				AutoDns: bridge.DHCP.AutoDNS, DnsSuffix: bridge.DHCP.DNSSuffix, DnsRunning: bridge.DHCP.DNSRunning,
			},
		}
		for _, diagnostic := range bridge.Diagnostics {
			mapped.Diagnostics = append(mapped.Diagnostics, networkDiagnosticToProto(diagnostic))
		}
		for _, workload := range bridge.Workloads {
			mapped.Workloads = append(mapped.Workloads, &apprunnerv1.WorkloadAttachment{
				Id: workload.ID, Name: workload.Name, WorkloadType: workload.Type, Running: workload.Running,
			})
		}
		response.Bridges = append(response.Bridges, mapped)
	}
	return response
}

func networkDiagnosticToProto(diagnostic networkDiagnostic) *apprunnerv1.NetworkDiagnostic {
	status := apprunnerv1.DiagnosticStatus_DIAGNOSTIC_STATUS_UNSPECIFIED
	switch diagnostic.Status {
	case diagnosticPass:
		status = apprunnerv1.DiagnosticStatus_DIAGNOSTIC_STATUS_PASS
	case diagnosticWarning:
		status = apprunnerv1.DiagnosticStatus_DIAGNOSTIC_STATUS_WARNING
	case diagnosticFail:
		status = apprunnerv1.DiagnosticStatus_DIAGNOSTIC_STATUS_FAIL
	case diagnosticInfo:
		status = apprunnerv1.DiagnosticStatus_DIAGNOSTIC_STATUS_INFO
	}
	return &apprunnerv1.NetworkDiagnostic{
		Key: diagnostic.Key, Label: diagnostic.Label, Status: status,
		Detail: diagnostic.Detail, Remediation: diagnostic.Remediation,
	}
}

func pendingNetworkChangeToProto(pending *pendingNetworkChange) *apprunnerv1.PendingNetworkChange {
	if pending == nil {
		return nil
	}
	return &apprunnerv1.PendingNetworkChange{
		Id: pending.ID, Description: pending.Description, ExpiresAt: pending.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}
}

func networkChangeTypeFromProto(changeType apprunnerv1.NetworkChangeType) (networkChangeType, error) {
	switch changeType {
	case apprunnerv1.NetworkChangeType_NETWORK_CHANGE_TYPE_CREATE_BRIDGE:
		return networkChangeCreateBridge, nil
	case apprunnerv1.NetworkChangeType_NETWORK_CHANGE_TYPE_DELETE_BRIDGE:
		return networkChangeDeleteBridge, nil
	case apprunnerv1.NetworkChangeType_NETWORK_CHANGE_TYPE_SET_BRIDGE_UP:
		return networkChangeSetBridgeUp, nil
	case apprunnerv1.NetworkChangeType_NETWORK_CHANGE_TYPE_SET_BRIDGE_DOWN:
		return networkChangeSetBridgeDown, nil
	case apprunnerv1.NetworkChangeType_NETWORK_CHANGE_TYPE_ATTACH_INTERFACE:
		return networkChangeAttachInterface, nil
	case apprunnerv1.NetworkChangeType_NETWORK_CHANGE_TYPE_DETACH_INTERFACE:
		return networkChangeDetachInterface, nil
	default:
		return "", twirp.InvalidArgumentError("type", "unsupported network change")
	}
}
