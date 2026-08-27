package apprunner

import (
	"context"
	"errors"
	"fmt"
	"time"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"
	"github.com/twitchtv/twirp"
)

// appRunnerService implements the Twirp API on top of the application runtime.
type appRunnerService struct {
	manager      *vmManager
	networking   *networkManager
	storage      *storageService
	templates    *cloudInitTemplateManager
	capabilities func() hostCapabilities
}

func newAppRunnerService(runtime appRuntime) *appRunnerService {
	return &appRunnerService{
		manager: runtime.manager, networking: runtime.networking, storage: runtime.storage,
		templates: runtime.templates, capabilities: runtime.capabilities,
	}
}

func (s *appRunnerService) GetHostStatus(context.Context, *apprunnerv1.GetHostStatusRequest) (*apprunnerv1.GetHostStatusResponse, error) {
	status := s.capabilities()
	return &apprunnerv1.GetHostStatusResponse{Status: &apprunnerv1.HostStatus{
		QemuAvailable: status.QEMUAvailable,
		KvmAvailable:  status.KVMAvailable,
	}}, nil
}

func (s *appRunnerService) ListISOs(context.Context, *apprunnerv1.ListISOsRequest) (*apprunnerv1.ListISOsResponse, error) {
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

func (s *appRunnerService) ListDiskImages(context.Context, *apprunnerv1.ListDiskImagesRequest) (*apprunnerv1.ListDiskImagesResponse, error) {
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

func (s *appRunnerService) ListVMs(context.Context, *apprunnerv1.ListVMsRequest) (*apprunnerv1.ListVMsResponse, error) {
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
	vm, err := s.manager.Get(request.GetId())
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.GetVMResponse{VirtualMachine: virtualMachineToProto(vm)}, nil
}

func (s *appRunnerService) CreateVM(ctx context.Context, request *apprunnerv1.CreateVMRequest) (*apprunnerv1.CreateVMResponse, error) {
	options := createVMOptions{
		Name: request.GetName(), Description: request.GetDescription(), CPUs: request.GetCpus(), MemoryMiB: request.GetMemoryMib(),
		Firmware:  firmwareFromProto(request.GetFirmware()),
		CloudInit: vmCloudInit{Enabled: request.GetCloudInitEnabled(), UserData: request.GetCloudInitUserData()},
	}
	for _, disk := range request.GetDisks() {
		options.Disks = append(options.Disks, diskSpecFromProto(disk))
	}
	for _, cdrom := range request.GetCdroms() {
		options.CDROMs = append(options.CDROMs, vmCDROMSpec{ISOName: cdrom.GetIsoName()})
	}
	for _, nic := range request.GetNics() {
		spec, err := nicSpecFromProto(nic)
		if err != nil {
			return nil, err
		}
		options.NICs = append(options.NICs, spec)
	}
	vm, err := s.manager.Create(ctx, options)
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.CreateVMResponse{VirtualMachine: virtualMachineToProto(vm)}, nil
}

func (s *appRunnerService) CloneVM(ctx context.Context, request *apprunnerv1.CloneVMRequest) (*apprunnerv1.CloneVMResponse, error) {
	vm, err := s.manager.Clone(ctx, request.GetId(), request.GetName())
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.CloneVMResponse{VirtualMachine: virtualMachineToProto(vm)}, nil
}

func (s *appRunnerService) StartVM(_ context.Context, request *apprunnerv1.StartVMRequest) (*apprunnerv1.StartVMResponse, error) {
	vm, err := s.manager.Start(request.GetId())
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.StartVMResponse{VirtualMachine: virtualMachineToProto(vm)}, nil
}

func (s *appRunnerService) StopVM(_ context.Context, request *apprunnerv1.StopVMRequest) (*apprunnerv1.StopVMResponse, error) {
	vm, err := s.manager.Stop(request.GetId(), request.GetForce(), time.Duration(request.GetTimeoutSeconds())*time.Second)
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.StopVMResponse{VirtualMachine: virtualMachineToProto(vm)}, nil
}

func (s *appRunnerService) RebootVM(_ context.Context, request *apprunnerv1.RebootVMRequest) (*apprunnerv1.RebootVMResponse, error) {
	vm, err := s.manager.Reboot(request.GetId())
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.RebootVMResponse{VirtualMachine: virtualMachineToProto(vm)}, nil
}

func (s *appRunnerService) UpdateVM(_ context.Context, request *apprunnerv1.UpdateVMRequest) (*apprunnerv1.UpdateVMResponse, error) {
	vm, err := s.manager.Update(request.GetId(), request.GetName(), request.GetDescription(), request.GetCpus(), request.GetMemoryMib())
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.UpdateVMResponse{VirtualMachine: virtualMachineToProto(vm)}, nil
}

func (s *appRunnerService) AddVMNIC(_ context.Context, request *apprunnerv1.AddVMNICRequest) (*apprunnerv1.AddVMNICResponse, error) {
	spec, err := nicSpecFromProto(request.GetNic())
	if err != nil {
		return nil, err
	}
	vm, err := s.manager.AddNIC(request.GetId(), spec)
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.AddVMNICResponse{VirtualMachine: virtualMachineToProto(vm)}, nil
}

func (s *appRunnerService) UpdateVMNIC(_ context.Context, request *apprunnerv1.UpdateVMNICRequest) (*apprunnerv1.UpdateVMNICResponse, error) {
	spec, err := nicSpecFromProto(request.GetNic())
	if err != nil {
		return nil, err
	}
	vm, err := s.manager.UpdateNIC(request.GetId(), request.GetNicId(), spec)
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.UpdateVMNICResponse{VirtualMachine: virtualMachineToProto(vm)}, nil
}

func (s *appRunnerService) RemoveVMNIC(_ context.Context, request *apprunnerv1.RemoveVMNICRequest) (*apprunnerv1.RemoveVMNICResponse, error) {
	vm, err := s.manager.RemoveNIC(request.GetId(), request.GetNicId())
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.RemoveVMNICResponse{VirtualMachine: virtualMachineToProto(vm)}, nil
}

func (s *appRunnerService) AddVMDisk(ctx context.Context, request *apprunnerv1.AddVMDiskRequest) (*apprunnerv1.AddVMDiskResponse, error) {
	vm, err := s.manager.AddDisk(ctx, request.GetId(), diskSpecFromProto(request.GetDisk()))
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.AddVMDiskResponse{VirtualMachine: virtualMachineToProto(vm)}, nil
}

func (s *appRunnerService) RemoveVMDisk(_ context.Context, request *apprunnerv1.RemoveVMDiskRequest) (*apprunnerv1.RemoveVMDiskResponse, error) {
	vm, err := s.manager.RemoveDisk(request.GetId(), request.GetDiskId())
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.RemoveVMDiskResponse{VirtualMachine: virtualMachineToProto(vm)}, nil
}

func (s *appRunnerService) AddVMCDROM(_ context.Context, request *apprunnerv1.AddVMCDROMRequest) (*apprunnerv1.AddVMCDROMResponse, error) {
	vm, err := s.manager.AddCDROM(request.GetId(), request.GetIsoName())
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.AddVMCDROMResponse{VirtualMachine: virtualMachineToProto(vm)}, nil
}

func (s *appRunnerService) UpdateVMCDROM(_ context.Context, request *apprunnerv1.UpdateVMCDROMRequest) (*apprunnerv1.UpdateVMCDROMResponse, error) {
	vm, err := s.manager.UpdateCDROM(request.GetId(), request.GetCdromId(), request.GetIsoName())
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.UpdateVMCDROMResponse{VirtualMachine: virtualMachineToProto(vm)}, nil
}

func (s *appRunnerService) RemoveVMCDROM(_ context.Context, request *apprunnerv1.RemoveVMCDROMRequest) (*apprunnerv1.RemoveVMCDROMResponse, error) {
	vm, err := s.manager.RemoveCDROM(request.GetId(), request.GetCdromId())
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.RemoveVMCDROMResponse{VirtualMachine: virtualMachineToProto(vm)}, nil
}

func (s *appRunnerService) ConfigureVMCloudInit(ctx context.Context, request *apprunnerv1.ConfigureVMCloudInitRequest) (*apprunnerv1.ConfigureVMCloudInitResponse, error) {
	vm, err := s.manager.ConfigureCloudInit(ctx, request.GetId(), vmCloudInit{
		Enabled: request.GetEnabled(), UserData: request.GetUserData(),
	})
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.ConfigureVMCloudInitResponse{VirtualMachine: virtualMachineToProto(vm)}, nil
}

func (s *appRunnerService) ListCloudInitTemplates(context.Context, *apprunnerv1.ListCloudInitTemplatesRequest) (*apprunnerv1.ListCloudInitTemplatesResponse, error) {
	response := &apprunnerv1.ListCloudInitTemplatesResponse{}
	for _, template := range s.templates.List() {
		response.Templates = append(response.Templates, cloudInitTemplateToProto(template))
	}
	return response, nil
}

func (s *appRunnerService) CreateCloudInitTemplate(_ context.Context, request *apprunnerv1.CreateCloudInitTemplateRequest) (*apprunnerv1.CreateCloudInitTemplateResponse, error) {
	template, err := s.templates.Create(request.GetName(), request.GetUserData())
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.CreateCloudInitTemplateResponse{Template: cloudInitTemplateToProto(template)}, nil
}

func (s *appRunnerService) UpdateCloudInitTemplate(_ context.Context, request *apprunnerv1.UpdateCloudInitTemplateRequest) (*apprunnerv1.UpdateCloudInitTemplateResponse, error) {
	template, err := s.templates.Update(request.GetName(), request.GetNewName(), request.GetUserData())
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.UpdateCloudInitTemplateResponse{Template: cloudInitTemplateToProto(template)}, nil
}

func (s *appRunnerService) DeleteCloudInitTemplate(_ context.Context, request *apprunnerv1.DeleteCloudInitTemplateRequest) (*apprunnerv1.DeleteCloudInitTemplateResponse, error) {
	if err := s.templates.Delete(request.GetName()); err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.DeleteCloudInitTemplateResponse{}, nil
}

func (s *appRunnerService) ConfigureVMIPMI(_ context.Context, request *apprunnerv1.ConfigureVMIPMIRequest) (*apprunnerv1.ConfigureVMIPMIResponse, error) {
	vm, err := s.manager.ConfigureIPMI(request.GetId(), vmIPMIConfig{
		Enabled: request.GetEnabled(), BridgeName: request.GetBridgeName(), Address: request.GetAddress(),
		Username: request.GetUsername(), Password: request.GetPassword(),
	})
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.ConfigureVMIPMIResponse{VirtualMachine: virtualMachineToProto(vm)}, nil
}

func (s *appRunnerService) DeleteVM(_ context.Context, request *apprunnerv1.DeleteVMRequest) (*apprunnerv1.DeleteVMResponse, error) {
	if err := s.manager.Delete(request.GetId()); err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.DeleteVMResponse{Id: request.GetId()}, nil
}

func (s *appRunnerService) GetNetworkingStatus(context.Context, *apprunnerv1.GetNetworkingStatusRequest) (*apprunnerv1.GetNetworkingStatusResponse, error) {
	status, err := s.networking.Status()
	if err != nil {
		return nil, rpcError(err)
	}
	return &apprunnerv1.GetNetworkingStatusResponse{Status: networkingStatusToProto(status)}, nil
}

func (s *appRunnerService) ApplyNetworkChange(ctx context.Context, request *apprunnerv1.ApplyNetworkChangeRequest) (*apprunnerv1.ApplyNetworkChangeResponse, error) {
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
	if err := s.networking.ConfigureBridgeSettings(
		request.GetBridgeName(), request.GetDescription(), request.GetEnabled(), request.GetCidr(), request.GetNatEnabled(),
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

func (s *appRunnerService) ClearBridgeDHCPLeases(ctx context.Context, request *apprunnerv1.ClearBridgeDHCPLeasesRequest) (*apprunnerv1.ClearBridgeDHCPLeasesResponse, error) {
	status, err := s.updateBridgeDHCPLeases(request.GetBridgeName(), func() error {
		return s.networking.ClearBridgeDHCPLeases(request.GetBridgeName())
	})
	if err != nil {
		return nil, err
	}
	return &apprunnerv1.ClearBridgeDHCPLeasesResponse{Status: networkingStatusToProto(status)}, nil
}

func (s *appRunnerService) DeleteBridgeDHCPLease(ctx context.Context, request *apprunnerv1.DeleteBridgeDHCPLeaseRequest) (*apprunnerv1.DeleteBridgeDHCPLeaseResponse, error) {
	status, err := s.updateBridgeDHCPLeases(request.GetBridgeName(), func() error {
		return s.networking.DeleteBridgeDHCPLease(request.GetBridgeName(), request.GetHardwareAddress())
	})
	if err != nil {
		return nil, err
	}
	return &apprunnerv1.DeleteBridgeDHCPLeaseResponse{Status: networkingStatusToProto(status)}, nil
}

func (s *appRunnerService) SetBridgeDHCPStaticLease(ctx context.Context, request *apprunnerv1.SetBridgeDHCPStaticLeaseRequest) (*apprunnerv1.SetBridgeDHCPStaticLeaseResponse, error) {
	status, err := s.updateBridgeDHCPLeases(request.GetBridgeName(), func() error {
		return s.networking.SetBridgeDHCPStaticLease(request.GetBridgeName(), request.GetHardwareAddress(), request.GetAddress())
	})
	if err != nil {
		return nil, err
	}
	return &apprunnerv1.SetBridgeDHCPStaticLeaseResponse{Status: networkingStatusToProto(status)}, nil
}

func (s *appRunnerService) updateBridgeDHCPLeases(bridge string, update func() error) (networkingStatus, error) {
	if err := update(); err != nil {
		return networkingStatus{}, twirp.NewError(twirp.FailedPrecondition, err.Error())
	}
	status, err := s.networking.Status()
	if err != nil {
		return networkingStatus{}, rpcError(err)
	}
	return status, nil
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
	result := &apprunnerv1.VirtualMachine{
		Id: vm.ID, Name: vm.Name, Description: vm.Description, Cpus: vm.CPUs, MemoryMib: vm.MemoryMiB,
		Firmware: firmwareToProto(vm.Firmware), Status: status,
		CreatedAt: vm.CreatedAt.Format(time.RFC3339), LastError: vm.LastError,
		ConsolePath: "/console/" + vm.ID,
		Ipmi: &apprunnerv1.VMIPMIStatus{
			Enabled: vm.IPMI.Enabled, BridgeName: vm.IPMI.BridgeName, Address: vm.IPMI.Address,
			Port: ipmiPort, Username: vm.IPMI.Username, Running: vm.IPMIRunning, LastError: vm.IPMIError,
		},
		CloudInit: &apprunnerv1.VMCloudInit{Enabled: vm.CloudInit.Enabled, UserData: vm.CloudInit.UserData},
	}
	for _, disk := range vm.Disks {
		result.Disks = append(result.Disks, &apprunnerv1.VMDisk{Id: disk.ID, SizeGib: disk.SizeGiB, SourceImage: disk.SourceImage})
	}
	for _, cdrom := range vm.CDROMs {
		result.Cdroms = append(result.Cdroms, &apprunnerv1.VMCDROM{Id: cdrom.ID, IsoName: cdrom.ISOName})
	}
	for _, nic := range vm.NICs {
		result.Nics = append(result.Nics, &apprunnerv1.VMNIC{
			Id: nic.ID, Mode: networkModeToProto(nic.Mode), BridgeName: nic.BridgeName, MacAddress: nic.MACAddress,
		})
	}
	return result
}

func firmwareToProto(firmware vmFirmware) apprunnerv1.VMFirmware {
	switch firmware {
	case vmFirmwareBIOS:
		return apprunnerv1.VMFirmware_VM_FIRMWARE_BIOS
	case vmFirmwareUEFI:
		return apprunnerv1.VMFirmware_VM_FIRMWARE_UEFI
	default:
		return apprunnerv1.VMFirmware_VM_FIRMWARE_UNSPECIFIED
	}
}

func firmwareFromProto(firmware apprunnerv1.VMFirmware) vmFirmware {
	switch firmware {
	case apprunnerv1.VMFirmware_VM_FIRMWARE_BIOS:
		return vmFirmwareBIOS
	case apprunnerv1.VMFirmware_VM_FIRMWARE_UEFI:
		return vmFirmwareUEFI
	default:
		return ""
	}
}

func networkModeToProto(mode networkMode) apprunnerv1.NetworkMode {
	switch mode {
	case networkModeNAT:
		return apprunnerv1.NetworkMode_NETWORK_MODE_NAT
	case networkModeBridge:
		return apprunnerv1.NetworkMode_NETWORK_MODE_BRIDGE
	default:
		return apprunnerv1.NetworkMode_NETWORK_MODE_UNSPECIFIED
	}
}

func diskSpecFromProto(disk *apprunnerv1.VMDiskSpec) vmDiskSpec {
	return vmDiskSpec{SizeGiB: disk.GetSizeGib(), SourceImage: disk.GetSourceImage()}
}

func nicSpecFromProto(nic *apprunnerv1.VMNICSpec) (vmNICSpec, error) {
	mode, err := networkModeFromProto(nic.GetMode())
	if err != nil {
		return vmNICSpec{}, err
	}
	return vmNICSpec{Mode: mode, BridgeName: nic.GetBridgeName(), MACAddress: nic.GetMacAddress()}, nil
}

func cloudInitTemplateToProto(template cloudInitTemplate) *apprunnerv1.CloudInitTemplate {
	return &apprunnerv1.CloudInitTemplate{
		Name: template.Name, UserData: template.UserData,
		CreatedAt: template.CreatedAt.Format(time.RFC3339), UpdatedAt: template.UpdatedAt.Format(time.RFC3339),
	}
}

func networkModeFromProto(mode apprunnerv1.NetworkMode) (networkMode, error) {
	switch mode {
	case apprunnerv1.NetworkMode_NETWORK_MODE_NAT:
		return networkModeNAT, nil
	case apprunnerv1.NetworkMode_NETWORK_MODE_BRIDGE:
		return networkModeBridge, nil
	default:
		return "", twirp.InvalidArgumentError("mode", "must be NAT or bridge")
	}
}

func rpcError(err error) error {
	var invalidField *fieldError
	switch {
	case errors.As(err, &invalidField):
		return twirp.InvalidArgumentError(invalidField.field, invalidField.message)
	case errors.Is(err, errVMNotFound):
		return twirp.NotFoundError(err.Error())
	case errors.Is(err, errCloudInitTemplateNotFound):
		return twirp.NotFoundError(err.Error())
	case errors.Is(err, errVMNameExists), errors.Is(err, errCloudInitTemplateExists):
		return twirp.NewError(twirp.AlreadyExists, err.Error())
	case errors.Is(err, errVMAlreadyRunning), errors.Is(err, errVMNotRunning), errors.Is(err, errBridgeUnavailable),
		errors.Is(err, errHostUnavailable), errors.Is(err, errDeleteRunningVM), errors.Is(err, errDeviceRunningVM),
		errors.Is(err, errCloneRunningVM), errors.Is(err, errVMBusy):
		return twirp.NewError(twirp.FailedPrecondition, err.Error())
	default:
		return twirp.InternalError(fmt.Sprintf("operation failed: %v", err))
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
