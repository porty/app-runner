import type { VMStatus } from './api'

export function canAccessVMConsole(status: VMStatus): boolean {
  return status === 'VM_STATUS_RUNNING' || status === 'VM_STATUS_STOPPING'
}
