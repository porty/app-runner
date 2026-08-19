import { afterEach, describe, expect, it, vi } from 'vitest'

import { echo, ping } from './api'

describe('Twirp API client', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('calls the Ping method using Twirp JSON', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          message: 'App Runner backend is ready',
          serverTime: '2026-08-19T05:30:00Z',
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ),
    )
    vi.stubGlobal('fetch', fetchMock)

    await expect(ping()).resolves.toMatchObject({
      message: 'App Runner backend is ready',
    })
    expect(fetchMock).toHaveBeenCalledWith(
      '/twirp/apprunner.v1.AppRunnerService/Ping',
      expect.objectContaining({ method: 'POST', body: '{}' }),
    )
  })

  it('surfaces Twirp errors', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ code: 'invalid_argument', msg: 'a message is required' }), {
          status: 400,
          headers: { 'Content-Type': 'application/json' },
        }),
      ),
    )

    await expect(echo('')).rejects.toThrow('a message is required')
  })
})

