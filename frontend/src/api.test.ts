import { afterEach, describe, expect, it, vi } from 'vitest'

import { applyNetworkChange, configureBridgeDHCP, createVM, echo, getNetworkingStatus, listVMs, ping } from './api'

describe('Twirp API client', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('calls the Ping method using Twirp JSON', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          message: 'App Runner backend is ready',
          server_time: '2026-08-19T05:30:00Z',
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

  it('returns an empty VM list when the repeated field is omitted', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }),
      ),
    )

    await expect(listVMs()).resolves.toEqual([])
  })

  it('sends VM creation fields using protobuf JSON names', async () => {
    const virtualMachine = {
      id: 'vm-id',
      name: 'Development',
      cpus: 2,
      memory_mib: 2048,
      disk_gib: 20,
      iso_name: 'installer.iso',
      network_mode: 'NETWORK_MODE_NAT' as const,
      status: 'VM_STATUS_STOPPED' as const,
      created_at: '2026-08-19T05:30:00Z',
      console_path: '/console/vm-id',
    }
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ virtual_machine: virtualMachine }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    vi.stubGlobal('fetch', fetchMock)

    await expect(
      createVM({
        name: 'Development',
        cpus: 2,
        memory_mib: 2048,
        disk_gib: 20,
        iso_name: 'installer.iso',
        network_mode: 'NETWORK_MODE_NAT',
      }),
    ).resolves.toEqual(virtualMachine)
    expect(fetchMock).toHaveBeenCalledWith(
      '/twirp/apprunner.v1.AppRunnerService/CreateVM',
      expect.objectContaining({
        body: JSON.stringify({
          name: 'Development',
          cpus: 2,
          memory_mib: 2048,
          disk_gib: 20,
          iso_name: 'installer.iso',
          network_mode: 'NETWORK_MODE_NAT',
        }),
      }),
    )
  })

  it('loads structured network diagnostics', async () => {
    const status = {
      user: {
        username: 'app-runner', uid: 1000, is_root: false, has_cap_net_admin: false,
        has_cap_net_bind_service: false, has_cap_net_raw: false,
      },
      diagnostics: [],
      bridges: [],
      interfaces: [],
      can_manage: false,
    }
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ status }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      ),
    )

    await expect(getNetworkingStatus()).resolves.toEqual(status)
  })

  it('applies bridge changes with the requested migration behavior', async () => {
    const pending = {
      id: 'change-id',
      description: 'Attach eth0 to br0',
      expires_at: '2026-08-19T05:30:15Z',
    }
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ pending_change: pending }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    vi.stubGlobal('fetch', fetchMock)

    await expect(
      applyNetworkChange({
        type: 'NETWORK_CHANGE_TYPE_ATTACH_INTERFACE',
        bridge_name: 'br0',
        interface_name: 'eth0',
        migrate_addresses: true,
      }),
    ).resolves.toEqual(pending)
    expect(fetchMock).toHaveBeenCalledWith(
      '/twirp/apprunner.v1.AppRunnerService/ApplyNetworkChange',
      expect.objectContaining({
        body: JSON.stringify({
          type: 'NETWORK_CHANGE_TYPE_ATTACH_INTERFACE',
          bridge_name: 'br0',
          interface_name: 'eth0',
          migrate_addresses: true,
        }),
      }),
    )
  })

  it('configures DHCP for a specific bridge', async () => {
    const status = {
      user: {
        username: 'app-runner', uid: 1000, is_root: false, has_cap_net_admin: true,
        has_cap_net_bind_service: true, has_cap_net_raw: true,
      },
      bridges: [],
      can_manage: true,
    }
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ status }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    vi.stubGlobal('fetch', fetchMock)

    await expect(configureBridgeDHCP('br0', true, '192.168.100.0/24')).resolves.toEqual(status)
    expect(fetchMock).toHaveBeenCalledWith(
      '/twirp/apprunner.v1.AppRunnerService/ConfigureBridgeDHCP',
      expect.objectContaining({
        body: JSON.stringify({ bridge_name: 'br0', enabled: true, cidr: '192.168.100.0/24' }),
      }),
    )
  })
})
