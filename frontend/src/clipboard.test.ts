import { afterEach, describe, expect, it, vi } from 'vitest'

import { copyText } from './clipboard'

afterEach(() => vi.unstubAllGlobals())

describe('copyText', () => {
  it('uses the Clipboard API when available', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })

    await copyText('192.168.100.50')

    expect(writeText).toHaveBeenCalledWith('192.168.100.50')
  })

  it('falls back to a temporary text area when the Clipboard API is unavailable', async () => {
    const select = vi.fn()
    const remove = vi.fn()
    const input = { value: '', setAttribute: vi.fn(), style: {}, select, remove }
    const appendChild = vi.fn()
    const execCommand = vi.fn().mockReturnValue(true)
    vi.stubGlobal('navigator', {})
    vi.stubGlobal('document', {
      createElement: vi.fn().mockReturnValue(input),
      body: { appendChild },
      execCommand,
    })

    await copyText('192.168.100.50')

    expect(input.value).toBe('192.168.100.50')
    expect(select).toHaveBeenCalledOnce()
    expect(execCommand).toHaveBeenCalledWith('copy')
    expect(remove).toHaveBeenCalledOnce()
  })
})
