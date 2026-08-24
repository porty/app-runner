const servicePath = '/twirp/apprunner.v1.AppRunnerService'

export interface PingResponse {
  message: string
  server_time: string
}

export interface EchoResponse {
  message: string
}

export type VMStatus =
  | 'VM_STATUS_STOPPED'
  | 'VM_STATUS_RUNNING'
  | 'VM_STATUS_STOPPING'
  | 'VM_STATUS_ERROR'
  | 'VM_STATUS_UNSPECIFIED'

export type NetworkMode = 'NETWORK_MODE_NAT' | 'NETWORK_MODE_BRIDGE'

export interface VirtualMachine {
  id: string
  name: string
  description?: string
  cpus: number
  memory_mib: number
  disk_gib: number
  iso_name: string
  network_mode: NetworkMode
  bridge_name?: string
  mac_address: string
  status: VMStatus
  created_at: string
  last_error?: string
  console_path: string
  ipmi?: VMIPMIStatus
  disks?: VMDisk[]
  cdroms?: VMCDROM[]
  cloud_init?: VMCloudInit
}

export interface VMDisk {
  id: string
  size_gib: number
  system: boolean
}

export interface VMCDROM {
  id: string
  iso_name?: string
}

export interface VMCloudInit {
  enabled: boolean
  user_data: string
}

export interface UpdateVMRequest {
  id: string
  name: string
  description: string
  cpus: number
  memory_mib: number
}

export interface UpdateVMNetworkRequest {
  id: string
  network_mode: NetworkMode
  bridge_name: string
  mac_address: string
}

export interface VMIPMIStatus {
  enabled: boolean
  bridge_name?: string
  address?: string
  port: number
  username?: string
  running: boolean
  last_error?: string
}

export interface VMIPMIConfiguration {
  id: string
  enabled: boolean
  bridge_name: string
  address: string
  username: string
  password: string
}

export interface VMCloudInitConfiguration {
  id: string
  enabled: boolean
  user_data: string
}

export interface CloudInitTemplate {
  name: string
  user_data: string
  created_at: string
  updated_at: string
}

export interface HostStatus {
  qemu_available: boolean
  kvm_available: boolean
  bridge_available: boolean
  bridge_name: string
  bridge_warning: string
}

export interface ISOImage {
  name: string
  size_bytes: string
}

export interface CreateVMRequest {
  name: string
  cpus: number
  memory_mib: number
  disk_gib: number
  iso_name: string
  network_mode: NetworkMode
  bridge_name?: string
  cloud_init_enabled: boolean
  cloud_init_user_data: string
}

export type DiagnosticStatus =
  | 'DIAGNOSTIC_STATUS_PASS'
  | 'DIAGNOSTIC_STATUS_WARNING'
  | 'DIAGNOSTIC_STATUS_FAIL'
  | 'DIAGNOSTIC_STATUS_INFO'
  | 'DIAGNOSTIC_STATUS_UNSPECIFIED'

export type NetworkChangeType =
  | 'NETWORK_CHANGE_TYPE_CREATE_BRIDGE'
  | 'NETWORK_CHANGE_TYPE_DELETE_BRIDGE'
  | 'NETWORK_CHANGE_TYPE_SET_BRIDGE_UP'
  | 'NETWORK_CHANGE_TYPE_SET_BRIDGE_DOWN'
  | 'NETWORK_CHANGE_TYPE_ATTACH_INTERFACE'
  | 'NETWORK_CHANGE_TYPE_DETACH_INTERFACE'

export interface NetworkDiagnostic {
  key: string
  label: string
  status: DiagnosticStatus
  detail: string
  remediation?: string
}

export interface UserIdentity {
  username: string
  uid: number
  groups?: string[]
  is_root: boolean
  has_cap_net_admin: boolean
  has_cap_net_bind_service: boolean
  has_cap_net_raw: boolean
}

export interface NetworkInterface {
  name: string
  is_up: boolean
  mtu: number
  hardware_address: string
  addresses?: string[]
  master?: string
  is_bridge: boolean
  can_attach: boolean
}

export interface WorkloadAttachment {
  id: string
  name: string
  workload_type: string
  running: boolean
}

export interface NetworkBridge {
  name: string
  description?: string
  is_up: boolean
  mtu: number
  hardware_address: string
  addresses?: string[]
  member_interfaces?: string[]
  workloads?: WorkloadAttachment[]
  diagnostics?: NetworkDiagnostic[]
  usable_by_qemu: boolean
  dhcp?: BridgeDHCPStatus
}

export interface BridgeDHCPStatus {
  enabled: boolean
  cidr: string
  server_address?: string
  pool_start?: string
  pool_end?: string
  running: boolean
  active_leases: number
  last_error?: string
  nat_enabled: boolean
  nat_running: boolean
  dns_enabled: boolean
  dns_forwarders?: string[]
  auto_dns: boolean
  dns_suffix?: string
  dns_running: boolean
}

export interface BridgeDHCPConfiguration {
  bridge_name: string
  description: string
  enabled: boolean
  cidr: string
  nat_enabled: boolean
  dns_enabled: boolean
  dns_forwarders: string[]
  auto_dns: boolean
  dns_suffix: string
}

export interface PendingNetworkChange {
  id: string
  description: string
  expires_at: string
}

export interface NetworkingStatus {
  user: UserIdentity
  diagnostics?: NetworkDiagnostic[]
  bridges?: NetworkBridge[]
  interfaces?: NetworkInterface[]
  pending_change?: PendingNetworkChange
  can_manage: boolean
}

export interface NetworkChangeRequest {
  type: NetworkChangeType
  bridge_name: string
  interface_name?: string
  migrate_addresses?: boolean
}

export type StorageRootName = 'iso' | 'disk'

export interface StorageNode {
  name: string
  path: string
  kind: 'file' | 'directory'
  size_bytes?: number
  modified_at?: string
  children?: StorageNode[]
}

export interface StorageRoot {
  name: StorageRootName
  entries: StorageNode[]
}

export interface StorageURLDownloadRequest {
  root: StorageRootName
  directory: string
  url: string
  filename?: string
}

interface StorageProtoNode {
  name: string
  path: string
  kind: 'STORAGE_ENTRY_KIND_FILE' | 'STORAGE_ENTRY_KIND_DIRECTORY' | 'STORAGE_ENTRY_KIND_UNSPECIFIED'
  size_bytes?: string
  modified_at?: string
  children?: StorageProtoNode[]
}

interface TwirpErrorBody {
  code?: string
  msg?: string
}

async function storageResponse<Result>(response: Response): Promise<Result> {
  if (!response.ok) {
    let message = response.statusText || 'The storage request failed'
    try {
      const body = (await response.json()) as { error?: string }
      message = body.error ?? message
    } catch {
      // Preserve the HTTP status as a useful fallback for non-JSON errors.
    }
    throw new Error(message)
  }
  return (await response.json()) as Result
}

async function callTwirp<Response>(
  method: string,
  request: object,
): Promise<Response> {
  const response = await fetch(`${servicePath}/${method}`, {
    method: 'POST',
    headers: {
      Accept: 'application/json',
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(request),
  })

  if (!response.ok) {
    let error: TwirpErrorBody = {}
    try {
      error = (await response.json()) as TwirpErrorBody
    } catch {
      // The status text remains a useful fallback for non-Twirp intermediary errors.
    }

    throw new Error(error.msg ?? response.statusText ?? 'The RPC request failed')
  }

  return (await response.json()) as Response
}

export function ping(): Promise<PingResponse> {
  return callTwirp<PingResponse>('Ping', {})
}

export function echo(message: string): Promise<EchoResponse> {
  return callTwirp<EchoResponse>('Echo', { message })
}

export async function getHostStatus(): Promise<HostStatus> {
  const response = await callTwirp<{ status: HostStatus }>('GetHostStatus', {})
  return response.status
}

export async function listISOs(): Promise<ISOImage[]> {
  const response = await callTwirp<{ images?: ISOImage[] }>('ListISOs', {})
  return response.images ?? []
}

export async function listVMs(): Promise<VirtualMachine[]> {
  const response = await callTwirp<{ virtual_machines?: VirtualMachine[] }>('ListVMs', {})
  return response.virtual_machines ?? []
}

export async function getVM(id: string): Promise<VirtualMachine> {
  const response = await callTwirp<{ virtual_machine: VirtualMachine }>('GetVM', { id })
  return response.virtual_machine
}

export async function createVM(request: CreateVMRequest): Promise<VirtualMachine> {
  const response = await callTwirp<{ virtual_machine: VirtualMachine }>('CreateVM', request)
  return response.virtual_machine
}

export async function cloneVM(id: string, name: string): Promise<VirtualMachine> {
  const response = await callTwirp<{ virtual_machine: VirtualMachine }>('CloneVM', { id, name })
  return response.virtual_machine
}

export async function startVM(id: string): Promise<VirtualMachine> {
  const response = await callTwirp<{ virtual_machine: VirtualMachine }>('StartVM', { id })
  return response.virtual_machine
}

export async function stopVM(id: string, force = false): Promise<VirtualMachine> {
  const response = await callTwirp<{ virtual_machine: VirtualMachine }>('StopVM', { id, force })
  return response.virtual_machine
}

export async function rebootVM(id: string): Promise<VirtualMachine> {
  const response = await callTwirp<{ virtual_machine: VirtualMachine }>('RebootVM', { id })
  return response.virtual_machine
}

export async function updateVM(request: UpdateVMRequest): Promise<VirtualMachine> {
  const response = await callTwirp<{ virtual_machine: VirtualMachine }>('UpdateVM', request)
  return response.virtual_machine
}

export async function updateVMNetwork(request: UpdateVMNetworkRequest): Promise<VirtualMachine> {
  const response = await callTwirp<{ virtual_machine: VirtualMachine }>('UpdateVMNetwork', request)
  return response.virtual_machine
}

export async function addVMDisk(id: string, sizeGib: number): Promise<VirtualMachine> {
  const response = await callTwirp<{ virtual_machine: VirtualMachine }>('AddVMDisk', { id, size_gib: sizeGib })
  return response.virtual_machine
}

export async function removeVMDisk(id: string, diskID: string): Promise<VirtualMachine> {
  const response = await callTwirp<{ virtual_machine: VirtualMachine }>('RemoveVMDisk', { id, disk_id: diskID })
  return response.virtual_machine
}

export async function addVMCDROM(id: string, isoName = ''): Promise<VirtualMachine> {
  const response = await callTwirp<{ virtual_machine: VirtualMachine }>('AddVMCDROM', { id, iso_name: isoName })
  return response.virtual_machine
}

export async function updateVMCDROM(id: string, cdromID: string, isoName: string): Promise<VirtualMachine> {
  const response = await callTwirp<{ virtual_machine: VirtualMachine }>('UpdateVMCDROM', { id, cdrom_id: cdromID, iso_name: isoName })
  return response.virtual_machine
}

export async function removeVMCDROM(id: string, cdromID: string): Promise<VirtualMachine> {
  const response = await callTwirp<{ virtual_machine: VirtualMachine }>('RemoveVMCDROM', { id, cdrom_id: cdromID })
  return response.virtual_machine
}

export async function configureVMIPMI(configuration: VMIPMIConfiguration): Promise<VirtualMachine> {
  const response = await callTwirp<{ virtual_machine: VirtualMachine }>('ConfigureVMIPMI', configuration)
  return response.virtual_machine
}

export async function configureVMCloudInit(configuration: VMCloudInitConfiguration): Promise<VirtualMachine> {
  const response = await callTwirp<{ virtual_machine: VirtualMachine }>('ConfigureVMCloudInit', configuration)
  return response.virtual_machine
}

export async function listCloudInitTemplates(): Promise<CloudInitTemplate[]> {
  const response = await callTwirp<{ templates?: CloudInitTemplate[] }>('ListCloudInitTemplates', {})
  return response.templates ?? []
}

export async function createCloudInitTemplate(name: string, userData: string): Promise<CloudInitTemplate> {
  const response = await callTwirp<{ template: CloudInitTemplate }>('CreateCloudInitTemplate', { name, user_data: userData })
  return response.template
}

export async function updateCloudInitTemplate(name: string, newName: string, userData: string): Promise<CloudInitTemplate> {
  const response = await callTwirp<{ template: CloudInitTemplate }>('UpdateCloudInitTemplate', { name, new_name: newName, user_data: userData })
  return response.template
}

export async function deleteCloudInitTemplate(name: string): Promise<void> {
  await callTwirp('DeleteCloudInitTemplate', { name })
}

export async function deleteVM(id: string): Promise<void> {
  await callTwirp<{ id: string }>('DeleteVM', { id })
}

export async function getNetworkingStatus(): Promise<NetworkingStatus> {
  const response = await callTwirp<{ status: NetworkingStatus }>('GetNetworkingStatus', {})
  return response.status
}

export async function applyNetworkChange(request: NetworkChangeRequest): Promise<PendingNetworkChange> {
  const response = await callTwirp<{ pending_change: PendingNetworkChange }>('ApplyNetworkChange', request)
  return response.pending_change
}

export async function confirmNetworkChange(id: string): Promise<NetworkingStatus> {
  const response = await callTwirp<{ status: NetworkingStatus }>('ConfirmNetworkChange', { id })
  return response.status
}

export async function revertNetworkChange(id: string): Promise<NetworkingStatus> {
  const response = await callTwirp<{ status: NetworkingStatus }>('RevertNetworkChange', { id })
  return response.status
}

export async function configureBridgeDHCP(configuration: BridgeDHCPConfiguration): Promise<NetworkingStatus> {
  const response = await callTwirp<{ status: NetworkingStatus }>('ConfigureBridgeDHCP', configuration)
  return response.status
}

export async function listStorage(): Promise<StorageRoot[]> {
  const response = await callTwirp<{ roots?: Array<{ name: StorageRootName; entries?: StorageProtoNode[] }> }>('ListStorage', {})
  return (response.roots ?? []).map((root) => ({
    name: root.name,
    entries: (root.entries ?? []).map(storageNodeFromProto),
  }))
}

export async function uploadStorageFiles(
  root: StorageRootName,
  directory: string,
  files: File[],
): Promise<StorageNode[]> {
  const body = new FormData()
  body.append('root', root)
  body.append('directory', directory)
  files.forEach((file) => body.append('files', file))
  const response = await storageResponse<{ uploaded?: StorageNode[] }>(await fetch('/api/storage/upload', {
    method: 'POST',
    body,
  }))
  return response.uploaded ?? []
}

export async function downloadStorageFile(request: StorageURLDownloadRequest): Promise<StorageNode> {
  const response = await callTwirp<{ file: StorageProtoNode }>('DownloadStorageFile', request)
  return storageNodeFromProto(response.file)
}

function storageNodeFromProto(node: StorageProtoNode): StorageNode {
  return {
    name: node.name,
    path: node.path,
    kind: node.kind === 'STORAGE_ENTRY_KIND_DIRECTORY' ? 'directory' : 'file',
    size_bytes: Number(node.size_bytes ?? 0),
    modified_at: node.modified_at,
    children: node.children?.map(storageNodeFromProto),
  }
}
