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
	capabilities func() hostCapabilities
	now          func() time.Time
}

func newAppRunnerService(manager *vmManager, capabilities func() hostCapabilities) *appRunnerService {
	return &appRunnerService{manager: manager, capabilities: capabilities, now: time.Now}
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
		IsoName: vm.ISOName, NetworkMode: mode, Status: status,
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
