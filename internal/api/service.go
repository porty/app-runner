// Package api implements the Twirp service, the console WebSocket proxy,
// and the HTTP handler that serves the embedded frontend.
package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"
	"github.com/twitchtv/twirp"

	"github.com/roflware/app-runner/internal/cloudinit"
	"github.com/roflware/app-runner/internal/dhcp"
	"github.com/roflware/app-runner/internal/dns"
	"github.com/roflware/app-runner/internal/hostnet"
	"github.com/roflware/app-runner/internal/ipmi"
	"github.com/roflware/app-runner/internal/storage"
	"github.com/roflware/app-runner/internal/validate"
	"github.com/roflware/app-runner/internal/vm"
)

// Service implements the Twirp API on top of the application runtime.
type Service struct {
	manager      *vm.Manager
	networking   *hostnet.Manager
	storage      *storage.Service
	templates    *cloudinit.TemplateManager
	capabilities func() vm.HostCapabilities
}

func NewService(runtime Runtime) *Service {
	return &Service{
		manager: runtime.VMs, networking: runtime.Networking, storage: runtime.Storage,
		templates: runtime.Templates, capabilities: runtime.Capabilities,
	}
}

func (s *Service) GetHostStatus(context.Context, *apprunnerv1.GetHostStatusRequest) (*apprunnerv1.GetHostStatusResponse, error) {
	status := s.capabilities()
	return &apprunnerv1.GetHostStatusResponse{Status: &apprunnerv1.HostStatus{
		QemuAvailable: status.QEMUAvailable,
		KvmAvailable:  status.KVMAvailable,
	}}, nil
}

func (s *Service) ListISOs(context.Context, *apprunnerv1.ListISOsRequest) (*apprunnerv1.ListISOsResponse, error) {
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

func (s *Service) ListDiskImages(context.Context, *apprunnerv1.ListDiskImagesRequest) (*apprunnerv1.ListDiskImagesResponse, error) {
	images, err := s.manager.ListDiskImages()
	if err != nil {
		return nil, rpcError(err)
	}
	response := &apprunnerv1.ListDiskImagesResponse{Images: make([]*apprunnerv1.DiskImage, 0, len(images))}
	for _, image := range images {
		response.Images = append(response.Images, &apprunnerv1.DiskImage{Name: image.Name, SizeBytes: image.SizeBytes})
	}
	return response, nil
}

func (s *Service) ListVMs(context.Context, *apprunnerv1.ListVMsRequest) (*apprunnerv1.ListVMsResponse, error) {
	vms, err := s.manager.List()
	if err != nil {
		return nil, rpcError(err)
	}
	response := &apprunnerv1.ListVMsResponse{VirtualMachines: make([]*apprunnerv1.VirtualMachine, 0, len(vms))}
	for _, machine := range vms {
		response.VirtualMachines = append(response.VirtualMachines, virtualMachineToProto(machine))
	}
	return response, nil
}

func (s *Service) GetVM(_ context.Context, request *apprunnerv1.GetVMRequest) (*apprunnerv1.GetVMResponse, error) {
	machine, err := s.manager.Get(request.GetId())
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.GetVMResponse{VirtualMachine: virtualMachineToProto(machine)}, nil
}

func (s *Service) CreateVM(ctx context.Context, request *apprunnerv1.CreateVMRequest) (*apprunnerv1.CreateVMResponse, error) {
	options := vm.CreateOptions{
		Name: request.GetName(), Description: request.GetDescription(), CPUs: request.GetCpus(), MemoryMiB: request.GetMemoryMib(),
		Firmware:  firmwareFromProto(request.GetFirmware()),
		CloudInit: vm.CloudInit{Enabled: request.GetCloudInitEnabled(), UserData: request.GetCloudInitUserData()},
	}
	for _, disk := range request.GetDisks() {
		options.Disks = append(options.Disks, diskSpecFromProto(disk))
	}
	for _, cdrom := range request.GetCdroms() {
		options.CDROMs = append(options.CDROMs, vm.CDROMSpec{ISOName: cdrom.GetIsoName()})
	}
	for _, nic := range request.GetNics() {
		spec, err := nicSpecFromProto(nic)
		if err != nil {
			return nil, err
		}
		options.NICs = append(options.NICs, spec)
	}
	machine, err := s.manager.Create(ctx, options)
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.CreateVMResponse{VirtualMachine: virtualMachineToProto(machine)}, nil
}

func (s *Service) CloneVM(ctx context.Context, request *apprunnerv1.CloneVMRequest) (*apprunnerv1.CloneVMResponse, error) {
	machine, err := s.manager.Clone(ctx, request.GetId(), request.GetName())
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.CloneVMResponse{VirtualMachine: virtualMachineToProto(machine)}, nil
}

func (s *Service) StartVM(_ context.Context, request *apprunnerv1.StartVMRequest) (*apprunnerv1.StartVMResponse, error) {
	machine, err := s.manager.Start(request.GetId())
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.StartVMResponse{VirtualMachine: virtualMachineToProto(machine)}, nil
}

func (s *Service) StopVM(_ context.Context, request *apprunnerv1.StopVMRequest) (*apprunnerv1.StopVMResponse, error) {
	machine, err := s.manager.Stop(request.GetId(), request.GetForce(), time.Duration(request.GetTimeoutSeconds())*time.Second)
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.StopVMResponse{VirtualMachine: virtualMachineToProto(machine)}, nil
}

func (s *Service) RebootVM(_ context.Context, request *apprunnerv1.RebootVMRequest) (*apprunnerv1.RebootVMResponse, error) {
	machine, err := s.manager.Reboot(request.GetId())
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.RebootVMResponse{VirtualMachine: virtualMachineToProto(machine)}, nil
}

func (s *Service) UpdateVM(_ context.Context, request *apprunnerv1.UpdateVMRequest) (*apprunnerv1.UpdateVMResponse, error) {
	machine, err := s.manager.Update(request.GetId(), vm.Update{
		Name: request.Name, Description: request.Description, CPUs: request.Cpus, MemoryMiB: request.MemoryMib,
	})
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.UpdateVMResponse{VirtualMachine: virtualMachineToProto(machine)}, nil
}

func (s *Service) AddVMNIC(_ context.Context, request *apprunnerv1.AddVMNICRequest) (*apprunnerv1.AddVMNICResponse, error) {
	spec, err := nicSpecFromProto(request.GetNic())
	if err != nil {
		return nil, err
	}
	machine, err := s.manager.AddNIC(request.GetId(), spec)
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.AddVMNICResponse{VirtualMachine: virtualMachineToProto(machine)}, nil
}

func (s *Service) UpdateVMNIC(_ context.Context, request *apprunnerv1.UpdateVMNICRequest) (*apprunnerv1.UpdateVMNICResponse, error) {
	spec, err := nicSpecFromProto(request.GetNic())
	if err != nil {
		return nil, err
	}
	machine, err := s.manager.UpdateNIC(request.GetId(), request.GetNicId(), spec)
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.UpdateVMNICResponse{VirtualMachine: virtualMachineToProto(machine)}, nil
}

func (s *Service) RemoveVMNIC(_ context.Context, request *apprunnerv1.RemoveVMNICRequest) (*apprunnerv1.RemoveVMNICResponse, error) {
	machine, err := s.manager.RemoveNIC(request.GetId(), request.GetNicId())
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.RemoveVMNICResponse{VirtualMachine: virtualMachineToProto(machine)}, nil
}

func (s *Service) AddVMDisk(ctx context.Context, request *apprunnerv1.AddVMDiskRequest) (*apprunnerv1.AddVMDiskResponse, error) {
	machine, err := s.manager.AddDisk(ctx, request.GetId(), diskSpecFromProto(request.GetDisk()))
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.AddVMDiskResponse{VirtualMachine: virtualMachineToProto(machine)}, nil
}

func (s *Service) RemoveVMDisk(_ context.Context, request *apprunnerv1.RemoveVMDiskRequest) (*apprunnerv1.RemoveVMDiskResponse, error) {
	machine, err := s.manager.RemoveDisk(request.GetId(), request.GetDiskId())
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.RemoveVMDiskResponse{VirtualMachine: virtualMachineToProto(machine)}, nil
}

func (s *Service) AddVMCDROM(_ context.Context, request *apprunnerv1.AddVMCDROMRequest) (*apprunnerv1.AddVMCDROMResponse, error) {
	machine, err := s.manager.AddCDROM(request.GetId(), request.GetIsoName())
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.AddVMCDROMResponse{VirtualMachine: virtualMachineToProto(machine)}, nil
}

func (s *Service) UpdateVMCDROM(_ context.Context, request *apprunnerv1.UpdateVMCDROMRequest) (*apprunnerv1.UpdateVMCDROMResponse, error) {
	machine, err := s.manager.UpdateCDROM(request.GetId(), request.GetCdromId(), request.GetIsoName())
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.UpdateVMCDROMResponse{VirtualMachine: virtualMachineToProto(machine)}, nil
}

func (s *Service) RemoveVMCDROM(_ context.Context, request *apprunnerv1.RemoveVMCDROMRequest) (*apprunnerv1.RemoveVMCDROMResponse, error) {
	machine, err := s.manager.RemoveCDROM(request.GetId(), request.GetCdromId())
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.RemoveVMCDROMResponse{VirtualMachine: virtualMachineToProto(machine)}, nil
}

func (s *Service) ConfigureVMCloudInit(ctx context.Context, request *apprunnerv1.ConfigureVMCloudInitRequest) (*apprunnerv1.ConfigureVMCloudInitResponse, error) {
	machine, err := s.manager.ConfigureCloudInit(ctx, request.GetId(), vm.CloudInit{
		Enabled: request.GetEnabled(), UserData: request.GetUserData(),
	})
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.ConfigureVMCloudInitResponse{VirtualMachine: virtualMachineToProto(machine)}, nil
}

func (s *Service) ListCloudInitTemplates(context.Context, *apprunnerv1.ListCloudInitTemplatesRequest) (*apprunnerv1.ListCloudInitTemplatesResponse, error) {
	response := &apprunnerv1.ListCloudInitTemplatesResponse{}
	for _, template := range s.templates.List() {
		response.Templates = append(response.Templates, cloudInitTemplateToProto(template))
	}
	return response, nil
}

func (s *Service) CreateCloudInitTemplate(_ context.Context, request *apprunnerv1.CreateCloudInitTemplateRequest) (*apprunnerv1.CreateCloudInitTemplateResponse, error) {
	template, err := s.templates.Create(request.GetName(), request.GetUserData())
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.CreateCloudInitTemplateResponse{Template: cloudInitTemplateToProto(template)}, nil
}

func (s *Service) UpdateCloudInitTemplate(_ context.Context, request *apprunnerv1.UpdateCloudInitTemplateRequest) (*apprunnerv1.UpdateCloudInitTemplateResponse, error) {
	template, err := s.templates.Update(request.GetName(), request.GetNewName(), request.GetUserData())
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.UpdateCloudInitTemplateResponse{Template: cloudInitTemplateToProto(template)}, nil
}

func (s *Service) DeleteCloudInitTemplate(_ context.Context, request *apprunnerv1.DeleteCloudInitTemplateRequest) (*apprunnerv1.DeleteCloudInitTemplateResponse, error) {
	if err := s.templates.Delete(request.GetName()); err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.DeleteCloudInitTemplateResponse{}, nil
}

func (s *Service) ConfigureVMIPMI(_ context.Context, request *apprunnerv1.ConfigureVMIPMIRequest) (*apprunnerv1.ConfigureVMIPMIResponse, error) {
	machine, err := s.manager.ConfigureIPMI(request.GetId(), vm.IPMIConfig{
		Enabled: request.GetEnabled(), BridgeName: request.GetBridgeName(), Address: request.GetAddress(),
		Username: request.GetUsername(), Password: request.GetPassword(),
	})
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.ConfigureVMIPMIResponse{VirtualMachine: virtualMachineToProto(machine)}, nil
}

func (s *Service) DeleteVM(_ context.Context, request *apprunnerv1.DeleteVMRequest) (*apprunnerv1.DeleteVMResponse, error) {
	if err := s.manager.Delete(request.GetId()); err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.DeleteVMResponse{Id: request.GetId()}, nil
}

func (s *Service) GetNetworkingStatus(context.Context, *apprunnerv1.GetNetworkingStatusRequest) (*apprunnerv1.GetNetworkingStatusResponse, error) {
	status, err := s.networking.Status()
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.GetNetworkingStatusResponse{Status: networkingStatusToProto(status)}, nil
}

func (s *Service) ApplyNetworkChange(ctx context.Context, request *apprunnerv1.ApplyNetworkChangeRequest) (*apprunnerv1.ApplyNetworkChangeResponse, error) {
	changeType, err := changeTypeFromProto(request.GetType())
	if err != nil {
		return nil, err
	}
	change := hostnet.Change{
		Type: changeType, BridgeName: request.GetBridgeName(), InterfaceName: request.GetInterfaceName(),
		MigrateAddresses: request.GetMigrateAddresses(),
	}
	if err := hostnet.ValidateChange(change); err != nil {
		return nil, twirp.InvalidArgumentError("network_change", err.Error())
	}
	pending, err := s.networking.Apply(ctx, change)
	if err != nil {
		return nil, networkRPCError(err)
	}
	return &apprunnerv1.ApplyNetworkChangeResponse{PendingChange: pendingChangeToProto(&pending)}, nil
}

func (s *Service) ConfirmNetworkChange(ctx context.Context, request *apprunnerv1.ConfirmNetworkChangeRequest) (*apprunnerv1.ConfirmNetworkChangeResponse, error) {
	if err := s.networking.Confirm(request.GetId()); err != nil {
		return nil, networkRPCError(err)
	}
	status, err := s.networking.Status()
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.ConfirmNetworkChangeResponse{Status: networkingStatusToProto(status)}, nil
}

func (s *Service) RevertNetworkChange(ctx context.Context, request *apprunnerv1.RevertNetworkChangeRequest) (*apprunnerv1.RevertNetworkChangeResponse, error) {
	if err := s.networking.Revert(request.GetId()); err != nil {
		return nil, networkRPCError(err)
	}
	status, err := s.networking.Status()
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.RevertNetworkChangeResponse{Status: networkingStatusToProto(status)}, nil
}

func (s *Service) ConfigureBridge(ctx context.Context, request *apprunnerv1.ConfigureBridgeRequest) (*apprunnerv1.ConfigureBridgeResponse, error) {
	if err := s.networking.ConfigureBridgeSettings(
		request.GetBridgeName(), request.GetDescription(), request.GetDhcpEnabled(), request.GetCidr(), request.GetNatEnabled(),
		dns.Config{
			Enabled: request.GetDnsEnabled(), Forwarders: request.GetDnsForwarders(),
			Auto: request.GetAutoDns(), Suffix: request.GetDnsSuffix(),
		},
		request.GetHttpBootUrl(),
	); err != nil {
		return nil, networkRPCError(err)
	}
	status, err := s.networking.Status()
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.ConfigureBridgeResponse{Status: networkingStatusToProto(status)}, nil
}

func (s *Service) ListBridgeLeases(_ context.Context, request *apprunnerv1.ListBridgeLeasesRequest) (*apprunnerv1.ListBridgeLeasesResponse, error) {
	leases, err := s.networking.Leases(request.GetBridgeName())
	if err != nil {
		return nil, networkRPCError(err)
	}
	response := &apprunnerv1.ListBridgeLeasesResponse{Leases: make([]*apprunnerv1.DHCPLease, 0, len(leases))}
	for _, lease := range leases {
		response.Leases = append(response.Leases, dhcpLeaseToProto(lease))
	}
	return response, nil
}

func (s *Service) ClearBridgeDHCPLeases(ctx context.Context, request *apprunnerv1.ClearBridgeDHCPLeasesRequest) (*apprunnerv1.ClearBridgeDHCPLeasesResponse, error) {
	status, err := s.updateBridgeDHCPLeases(request.GetBridgeName(), func() error {
		return s.networking.ClearBridgeDHCPLeases(request.GetBridgeName())
	})
	if err != nil {
		return nil, err
	}
	return &apprunnerv1.ClearBridgeDHCPLeasesResponse{Status: networkingStatusToProto(status)}, nil
}

func (s *Service) DeleteBridgeDHCPLease(ctx context.Context, request *apprunnerv1.DeleteBridgeDHCPLeaseRequest) (*apprunnerv1.DeleteBridgeDHCPLeaseResponse, error) {
	status, err := s.updateBridgeDHCPLeases(request.GetBridgeName(), func() error {
		return s.networking.DeleteBridgeDHCPLease(request.GetBridgeName(), request.GetHardwareAddress())
	})
	if err != nil {
		return nil, err
	}
	return &apprunnerv1.DeleteBridgeDHCPLeaseResponse{Status: networkingStatusToProto(status)}, nil
}

func (s *Service) SetBridgeDHCPStaticLease(ctx context.Context, request *apprunnerv1.SetBridgeDHCPStaticLeaseRequest) (*apprunnerv1.SetBridgeDHCPStaticLeaseResponse, error) {
	status, err := s.updateBridgeDHCPLeases(request.GetBridgeName(), func() error {
		return s.networking.SetBridgeDHCPStaticLease(request.GetBridgeName(), request.GetHardwareAddress(), request.GetAddress())
	})
	if err != nil {
		return nil, err
	}
	return &apprunnerv1.SetBridgeDHCPStaticLeaseResponse{Status: networkingStatusToProto(status)}, nil
}

func (s *Service) updateBridgeDHCPLeases(bridge string, update func() error) (hostnet.Status, error) {
	if err := update(); err != nil {
		return hostnet.Status{}, networkRPCError(err)
	}
	status, err := s.networking.Status()
	if err != nil {
		return hostnet.Status{}, rpcError(err)
	}
	return status, nil
}

func virtualMachineToProto(machine vm.VirtualMachine) *apprunnerv1.VirtualMachine {
	status := apprunnerv1.VMStatus_VM_STATUS_UNSPECIFIED
	switch machine.Status {
	case vm.StatusStopped:
		status = apprunnerv1.VMStatus_VM_STATUS_STOPPED
	case vm.StatusRunning:
		status = apprunnerv1.VMStatus_VM_STATUS_RUNNING
	case vm.StatusStopping:
		status = apprunnerv1.VMStatus_VM_STATUS_STOPPING
	case vm.StatusError:
		status = apprunnerv1.VMStatus_VM_STATUS_ERROR
	}
	result := &apprunnerv1.VirtualMachine{
		Id: machine.ID, Name: machine.Name, Description: machine.Description, Cpus: machine.CPUs, MemoryMib: machine.MemoryMiB,
		Firmware: firmwareToProto(machine.Firmware), Status: status,
		CreatedAt: machine.CreatedAt.Format(time.RFC3339), LastError: machine.LastError,
		ConsolePath: "/console/" + machine.ID,
		Ipmi: &apprunnerv1.VMIPMIStatus{
			Enabled: machine.IPMI.Enabled, BridgeName: machine.IPMI.BridgeName, Address: machine.IPMI.Address,
			Port: ipmi.Port, Username: machine.IPMI.Username, Running: machine.IPMIRunning, LastError: machine.IPMIError,
		},
		CloudInit: &apprunnerv1.VMCloudInit{Enabled: machine.CloudInit.Enabled, UserData: machine.CloudInit.UserData},
	}
	for _, disk := range machine.Disks {
		result.Disks = append(result.Disks, &apprunnerv1.VMDisk{Id: disk.ID, SizeGib: disk.SizeGiB, SourceImage: disk.SourceImage})
	}
	for _, cdrom := range machine.CDROMs {
		result.Cdroms = append(result.Cdroms, &apprunnerv1.VMCDROM{Id: cdrom.ID, IsoName: cdrom.ISOName})
	}
	for _, nic := range machine.NICs {
		result.Nics = append(result.Nics, &apprunnerv1.VMNIC{
			Id: nic.ID, Mode: networkModeToProto(nic.Mode), BridgeName: nic.BridgeName, MacAddress: nic.MACAddress,
			Addresses: nic.Addresses,
		})
	}
	return result
}

func firmwareToProto(firmware vm.Firmware) apprunnerv1.VMFirmware {
	switch firmware {
	case vm.FirmwareBIOS:
		return apprunnerv1.VMFirmware_VM_FIRMWARE_BIOS
	case vm.FirmwareUEFI:
		return apprunnerv1.VMFirmware_VM_FIRMWARE_UEFI
	default:
		return apprunnerv1.VMFirmware_VM_FIRMWARE_UNSPECIFIED
	}
}

func firmwareFromProto(firmware apprunnerv1.VMFirmware) vm.Firmware {
	switch firmware {
	case apprunnerv1.VMFirmware_VM_FIRMWARE_BIOS:
		return vm.FirmwareBIOS
	case apprunnerv1.VMFirmware_VM_FIRMWARE_UEFI:
		return vm.FirmwareUEFI
	default:
		return ""
	}
}

func networkModeToProto(mode vm.NetworkMode) apprunnerv1.NetworkMode {
	switch mode {
	case vm.NetworkModeNAT:
		return apprunnerv1.NetworkMode_NETWORK_MODE_NAT
	case vm.NetworkModeBridge:
		return apprunnerv1.NetworkMode_NETWORK_MODE_BRIDGE
	default:
		return apprunnerv1.NetworkMode_NETWORK_MODE_UNSPECIFIED
	}
}

func diskSpecFromProto(disk *apprunnerv1.VMDiskSpec) vm.DiskSpec {
	return vm.DiskSpec{SizeGiB: disk.GetSizeGib(), SourceImage: disk.GetSourceImage()}
}

func nicSpecFromProto(nic *apprunnerv1.VMNICSpec) (vm.NICSpec, error) {
	mode, err := networkModeFromProto(nic.GetMode())
	if err != nil {
		return vm.NICSpec{}, err
	}
	return vm.NICSpec{Mode: mode, BridgeName: nic.GetBridgeName(), MACAddress: nic.GetMacAddress()}, nil
}

func cloudInitTemplateToProto(template cloudinit.Template) *apprunnerv1.CloudInitTemplate {
	return &apprunnerv1.CloudInitTemplate{
		Name: template.Name, UserData: template.UserData,
		CreatedAt: template.CreatedAt.Format(time.RFC3339), UpdatedAt: template.UpdatedAt.Format(time.RFC3339),
	}
}

func networkModeFromProto(mode apprunnerv1.NetworkMode) (vm.NetworkMode, error) {
	switch mode {
	case apprunnerv1.NetworkMode_NETWORK_MODE_NAT:
		return vm.NetworkModeNAT, nil
	case apprunnerv1.NetworkMode_NETWORK_MODE_BRIDGE:
		return vm.NetworkModeBridge, nil
	default:
		return "", twirp.InvalidArgumentError("mode", "must be NAT or bridge")
	}
}

// networkRPCError maps host networking and guest service errors. Unknown
// errors are reported as failed preconditions because they describe host
// state (conflicting addresses, missing bridges, running workloads) rather
// than server faults.
func networkRPCError(err error) error {
	var invalidField *validate.FieldError
	switch {
	case errors.As(err, &invalidField):
		return twirp.InvalidArgumentError(invalidField.Field, invalidField.Message)
	case errors.Is(err, hostnet.ErrBridgeNotFound), errors.Is(err, hostnet.ErrPendingChangeNotFound), errors.Is(err, dhcp.ErrLeaseNotFound), errors.Is(err, vm.ErrNotFound):
		return twirp.NotFoundError(err.Error())
	default:
		return twirp.NewError(twirp.FailedPrecondition, err.Error())
	}
}

func rpcError(err error) error {
	var invalidField *validate.FieldError
	switch {
	case errors.As(err, &invalidField):
		return twirp.InvalidArgumentError(invalidField.Field, invalidField.Message)
	case errors.Is(err, vm.ErrNotFound):
		return twirp.NotFoundError(err.Error())
	case errors.Is(err, cloudinit.ErrTemplateNotFound):
		return twirp.NotFoundError(err.Error())
	case errors.Is(err, vm.ErrNameExists), errors.Is(err, cloudinit.ErrTemplateExists):
		return twirp.NewError(twirp.AlreadyExists, err.Error())
	case errors.Is(err, vm.ErrAlreadyRunning), errors.Is(err, vm.ErrNotRunning), errors.Is(err, vm.ErrBridgeUnavailable),
		errors.Is(err, vm.ErrHostUnavailable), errors.Is(err, vm.ErrDeleteRunning), errors.Is(err, vm.ErrDeviceRunning),
		errors.Is(err, vm.ErrCloneRunning), errors.Is(err, vm.ErrBusy):
		return twirp.NewError(twirp.FailedPrecondition, err.Error())
	default:
		return twirp.InternalError(fmt.Sprintf("operation failed: %v", err))
	}
}

func networkingStatusToProto(status hostnet.Status) *apprunnerv1.NetworkingStatus {
	response := &apprunnerv1.NetworkingStatus{
		User: &apprunnerv1.UserIdentity{
			Username: status.User.Username, Uid: status.User.UID, Groups: status.User.Groups,
			IsRoot: status.User.IsRoot, HasCapNetAdmin: status.User.HasCAPNetAdmin,
			HasCapNetBindService: status.User.HasCAPNetBindService, HasCapNetRaw: status.User.HasCAPNetRaw,
		},
		CanManage:     status.CanManage,
		PendingChange: pendingChangeToProto(status.Pending),
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
			Name: bridge.Name, Description: bridge.Description, IsUp: bridge.IsUp, Mtu: bridge.MTU,
			HardwareAddress: bridge.HardwareAddress, Addresses: bridge.Addresses,
			MemberInterfaces: bridge.MemberInterfaces, UsableByQemu: bridge.UsableByQEMU,
			Dhcp: &apprunnerv1.BridgeDHCPStatus{
				Enabled: bridge.DHCP.Enabled, Cidr: bridge.DHCP.CIDR, ServerAddress: bridge.DHCP.ServerAddress,
				PoolStart: bridge.DHCP.PoolStart, PoolEnd: bridge.DHCP.PoolEnd, Running: bridge.DHCP.Running,
				ActiveLeases: bridge.DHCP.ActiveLeases, LastError: bridge.DHCP.LastError,
				NatEnabled: bridge.DHCP.NATEnabled, NatRunning: bridge.DHCP.NATRunning,
				DnsEnabled: bridge.DHCP.DNSEnabled, DnsForwarders: bridge.DHCP.DNSForwarders,
				AutoDns: bridge.DHCP.AutoDNS, DnsSuffix: bridge.DHCP.DNSSuffix, DnsRunning: bridge.DHCP.DNSRunning,
				HttpBootUrl: bridge.DHCP.HTTPBootURL,
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
		for _, lease := range bridge.DHCP.Leases {
			mapped.Dhcp.Leases = append(mapped.Dhcp.Leases, &apprunnerv1.DHCPLease{
				HardwareAddress: lease.HardwareAddress, Address: lease.Address, ExpiresAt: lease.ExpiresAt.UTC().Format(time.RFC3339),
				VmName: lease.VMName, IsStatic: lease.Static,
			})
		}
		response.Bridges = append(response.Bridges, mapped)
	}
	return response
}

func dhcpLeaseToProto(lease dhcp.Lease) *apprunnerv1.DHCPLease {
	return &apprunnerv1.DHCPLease{
		HardwareAddress: lease.HardwareAddress, Address: lease.Address, ExpiresAt: lease.ExpiresAt.UTC().Format(time.RFC3339),
		VmName: lease.VMName, IsStatic: lease.Static,
	}
}

func networkDiagnosticToProto(diagnostic hostnet.Diagnostic) *apprunnerv1.NetworkDiagnostic {
	status := apprunnerv1.DiagnosticStatus_DIAGNOSTIC_STATUS_UNSPECIFIED
	switch diagnostic.Status {
	case hostnet.DiagnosticPass:
		status = apprunnerv1.DiagnosticStatus_DIAGNOSTIC_STATUS_PASS
	case hostnet.DiagnosticWarning:
		status = apprunnerv1.DiagnosticStatus_DIAGNOSTIC_STATUS_WARNING
	case hostnet.DiagnosticFail:
		status = apprunnerv1.DiagnosticStatus_DIAGNOSTIC_STATUS_FAIL
	case hostnet.DiagnosticInfo:
		status = apprunnerv1.DiagnosticStatus_DIAGNOSTIC_STATUS_INFO
	}
	return &apprunnerv1.NetworkDiagnostic{
		Key: diagnostic.Key, Label: diagnostic.Label, Status: status,
		Detail: diagnostic.Detail, Remediation: diagnostic.Remediation,
	}
}

func pendingChangeToProto(pending *hostnet.PendingChange) *apprunnerv1.PendingNetworkChange {
	if pending == nil {
		return nil
	}
	return &apprunnerv1.PendingNetworkChange{
		Id: pending.ID, Description: pending.Description, ExpiresAt: pending.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}
}

func changeTypeFromProto(changeType apprunnerv1.NetworkChangeType) (hostnet.ChangeType, error) {
	switch changeType {
	case apprunnerv1.NetworkChangeType_NETWORK_CHANGE_TYPE_CREATE_BRIDGE:
		return hostnet.ChangeCreateBridge, nil
	case apprunnerv1.NetworkChangeType_NETWORK_CHANGE_TYPE_DELETE_BRIDGE:
		return hostnet.ChangeDeleteBridge, nil
	case apprunnerv1.NetworkChangeType_NETWORK_CHANGE_TYPE_SET_BRIDGE_UP:
		return hostnet.ChangeSetBridgeUp, nil
	case apprunnerv1.NetworkChangeType_NETWORK_CHANGE_TYPE_SET_BRIDGE_DOWN:
		return hostnet.ChangeSetBridgeDown, nil
	case apprunnerv1.NetworkChangeType_NETWORK_CHANGE_TYPE_ATTACH_INTERFACE:
		return hostnet.ChangeAttachInterface, nil
	case apprunnerv1.NetworkChangeType_NETWORK_CHANGE_TYPE_DETACH_INTERFACE:
		return hostnet.ChangeDetachInterface, nil
	default:
		return "", twirp.InvalidArgumentError("type", "unsupported network change")
	}
}
