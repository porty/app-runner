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
  cpus: number
  memory_mib: number
  disk_gib: number
  iso_name: string
  network_mode: NetworkMode
  status: VMStatus
  created_at: string
  last_error?: string
  console_path: string
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
}

interface TwirpErrorBody {
  code?: string
  msg?: string
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

export async function startVM(id: string): Promise<VirtualMachine> {
  const response = await callTwirp<{ virtual_machine: VirtualMachine }>('StartVM', { id })
  return response.virtual_machine
}

export async function stopVM(id: string, force = false): Promise<VirtualMachine> {
  const response = await callTwirp<{ virtual_machine: VirtualMachine }>('StopVM', { id, force })
  return response.virtual_machine
}

export async function deleteVM(id: string): Promise<void> {
  await callTwirp<{ id: string }>('DeleteVM', { id })
}
