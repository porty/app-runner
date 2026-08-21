import { describe, expect, it } from 'vitest'

import { canAccessVMConsole } from './vmConsoleAccess'

describe('canAccessVMConsole', () => {
  it('keeps console access available while a VM is stopping', () => {
    expect(canAccessVMConsole('VM_STATUS_RUNNING')).toBe(true)
    expect(canAccessVMConsole('VM_STATUS_STOPPING')).toBe(true)
    expect(canAccessVMConsole('VM_STATUS_STOPPED')).toBe(false)
  })
})
