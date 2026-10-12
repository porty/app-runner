import { afterEach, describe, expect, it, vi } from 'vitest'

import { addVMNIC, applyNetworkChange, cloneVM, configureBridge, configureVMCloudInit, configureVMIPMI, createCloudInitTemplate, createVM, deleteCloudInitTemplate, downloadStorageFile, getNetworkingStatus, listCloudInitTemplates, listDiskImages, listStorage, listVMs, rebootVM, updateCloudInitTemplate, uploadStorageFiles } from './api'

describe('Twirp API client', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('calls Twirp methods as JSON POST requests', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ virtual_machines: [] }), { status: 200, headers: { 'Content-Type': 'application/json' } }),
    )
    vi.stubGlobal('fetch', fetchMock)

    await expect(listVMs()).resolves.toEqual([])
    expect(fetchMock).toHaveBeenCalledWith(
      '/twirp/apprunner.v1.AppRunnerService/ListVMs',
      expect.objectContaining({ method: 'POST', body: '{}' }),
    )
  })

  it('surfaces Twirp errors', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ code: 'invalid_argument', msg: 'name: a name is required' }), {
          status: 400,
          headers: { 'Content-Type': 'application/json' },
        }),
      ),
    )

    await expect(cloneVM('vm-id', '')).rejects.toThrow('name: a name is required')
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
      disks: [{ id: 'd0', size_gib: 20 }],
      cdroms: [{ id: 'c0', iso_name: 'installer.iso' }],
      nics: [{ id: 'n0', mode: 'NETWORK_MODE_NAT' as const, mac_address: '52:54:00:00:00:01' }],
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

    const request = {
      name: 'Development',
      cpus: 2,
      memory_mib: 2048,
      disks: [{ size_gib: 20 }],
      cdroms: [{ iso_name: 'installer.iso' }],
      nics: [{ mode: 'NETWORK_MODE_NAT' as const }],
      cloud_init_enabled: true,
      cloud_init_user_data: '#cloud-config\npackages: [curl]\n',
    }
    await expect(createVM(request)).resolves.toEqual(virtualMachine)
    expect(fetchMock).toHaveBeenCalledWith(
      '/twirp/apprunner.v1.AppRunnerService/CreateVM',
      expect.objectContaining({ body: JSON.stringify(request) }),
    )
  })

  it('clones a VM with its requested new name', async () => {
    const virtualMachine = { id: 'clone-id', name: 'Development-copy', status: 'VM_STATUS_STOPPED' as const }
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ virtual_machine: virtualMachine }), {
      status: 200, headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(cloneVM('vm-id', 'Development-copy')).resolves.toEqual(virtualMachine)
    expect(fetchMock).toHaveBeenCalledWith(
      '/twirp/apprunner.v1.AppRunnerService/CloneVM',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ id: 'vm-id', name: 'Development-copy' }) }),
    )
  })

  it('adds a network interface through Twirp', async () => {
    const virtualMachine = { id: 'vm-id', name: 'Development', nics: [{ id: 'n0', mode: 'NETWORK_MODE_BRIDGE' as const, bridge_name: 'br0', mac_address: '52:54:00:00:00:01' }] }
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ virtual_machine: virtualMachine }), {
      status: 200, headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(addVMNIC('vm-id', { mode: 'NETWORK_MODE_BRIDGE', bridge_name: 'br0' })).resolves.toEqual(virtualMachine)
    expect(fetchMock).toHaveBeenCalledWith(
      '/twirp/apprunner.v1.AppRunnerService/AddVMNIC',
      expect.objectContaining({ body: JSON.stringify({ id: 'vm-id', nic: { mode: 'NETWORK_MODE_BRIDGE', bridge_name: 'br0' } }) }),
    )
  })

  it('reboots a VM through Twirp', async () => {
    const virtualMachine = { id: 'vm-id', name: 'Development', status: 'VM_STATUS_RUNNING' as const }
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ virtual_machine: virtualMachine }), {
      status: 200, headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(rebootVM('vm-id')).resolves.toEqual(virtualMachine)
    expect(fetchMock).toHaveBeenCalledWith(
      '/twirp/apprunner.v1.AppRunnerService/RebootVM',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ id: 'vm-id' }) }),
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

  it('configures a bridge and its guest network services', async () => {
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

    const configuration = {
      bridge_name: 'br0', description: 'VM network', dhcp_enabled: true, gateway_only: false, cidr: '192.168.100.0/24', nat_enabled: true,
      dns_enabled: true, dns_forwarders: ['1.1.1.1', '9.9.9.9'], auto_dns: true, dns_suffix: 'br0.internal',
      http_boot_url: 'http://192.168.100.50/boot/bootx64.efi',
    }
    await expect(configureBridge(configuration)).resolves.toEqual(status)
    expect(fetchMock).toHaveBeenCalledWith(
      '/twirp/apprunner.v1.AppRunnerService/ConfigureBridge',
      expect.objectContaining({
        body: JSON.stringify(configuration),
      }),
    )
  })

  it('configures a VM IPMI endpoint', async () => {
    const configuration = {
      id: 'vm-id', enabled: true, bridge_name: 'mgmt0', address: '192.168.50.2', username: 'admin', password: 'secret',
    }
    const virtualMachine = { id: 'vm-id', name: 'node-1', ipmi: { enabled: true, address: '192.168.50.2', port: 623, running: true } }
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ virtual_machine: virtualMachine }), {
      status: 200, headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(configureVMIPMI(configuration)).resolves.toEqual(virtualMachine)
    expect(fetchMock).toHaveBeenCalledWith(
      '/twirp/apprunner.v1.AppRunnerService/ConfigureVMIPMI',
      expect.objectContaining({ body: JSON.stringify(configuration) }),
    )
  })

  it('configures Cloud Init through Twirp', async () => {
    const configuration = { id: 'vm-id', enabled: true, user_data: '#cloud-config\npackages: [curl]\n' }
    const virtualMachine = { id: 'vm-id', name: 'node-1', cloud_init: { enabled: true, user_data: configuration.user_data } }
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ virtual_machine: virtualMachine }), {
      status: 200, headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(configureVMCloudInit(configuration)).resolves.toEqual(virtualMachine)
    expect(fetchMock).toHaveBeenCalledWith(
      '/twirp/apprunner.v1.AppRunnerService/ConfigureVMCloudInit',
      expect.objectContaining({ body: JSON.stringify(configuration) }),
    )
  })

  it('manages Cloud Init templates through Twirp', async () => {
    const template = { name: 'Ubuntu', user_data: '#cloud-config\n', created_at: '2026-08-24T00:00:00Z', updated_at: '2026-08-24T00:00:00Z' }
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ templates: [template] }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ template }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ template: { ...template, name: 'Ubuntu server' } }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
      .mockResolvedValueOnce(new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(listCloudInitTemplates()).resolves.toEqual([template])
    await expect(createCloudInitTemplate('Ubuntu', template.user_data)).resolves.toEqual(template)
    await expect(updateCloudInitTemplate('Ubuntu', 'Ubuntu server', template.user_data)).resolves.toEqual({ ...template, name: 'Ubuntu server' })
    await expect(deleteCloudInitTemplate('Ubuntu server')).resolves.toBeUndefined()

    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      '/twirp/apprunner.v1.AppRunnerService/ListCloudInitTemplates',
      '/twirp/apprunner.v1.AppRunnerService/CreateCloudInitTemplate',
      '/twirp/apprunner.v1.AppRunnerService/UpdateCloudInitTemplate',
      '/twirp/apprunner.v1.AppRunnerService/DeleteCloudInitTemplate',
    ])
  })

  it('lists cloud disk images through Twirp', async () => {
    const images = [{ name: 'ubuntu/resolute.img', size_bytes: '1073741824' }]
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ images }), {
      status: 200, headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(listDiskImages()).resolves.toEqual(images)
    expect(fetchMock).toHaveBeenCalledWith(
      '/twirp/apprunner.v1.AppRunnerService/ListDiskImages',
      expect.objectContaining({ body: '{}' }),
    )
  })

  it('loads the recursive storage roots', async () => {
    const rpcRoots = [{ name: 'iso', entries: [{ name: 'linux', path: 'linux', kind: 'STORAGE_ENTRY_KIND_DIRECTORY', children: [] }] }]
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ roots: rpcRoots }), {
      status: 200, headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(listStorage()).resolves.toEqual([
      { name: 'iso', entries: [{ name: 'linux', path: 'linux', kind: 'directory', size_bytes: 0, children: [] }] },
    ])
    expect(fetchMock).toHaveBeenCalledWith(
      '/twirp/apprunner.v1.AppRunnerService/ListStorage',
      expect.objectContaining({ method: 'POST', body: '{}' }),
    )
  })

  it('uploads browser files as multipart form data', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ uploaded: [] }), {
      status: 201, headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)
    const file = new File(['image'], 'installer.iso')

    await uploadStorageFiles('iso', 'linux', [file])
    const request = fetchMock.mock.calls[0]
    expect(request[0]).toBe('/api/storage/upload')
    expect(request[1]).toMatchObject({ method: 'POST' })
    expect(request[1].body.get('root')).toBe('iso')
    expect(request[1].body.get('directory')).toBe('linux')
    expect(request[1].body.get('files')).toBe(file)
  })

  it('requests a server-side URL download', async () => {
    const file = { name: 'installer.iso', path: 'installer.iso', kind: 'STORAGE_ENTRY_KIND_FILE', size_bytes: '5' }
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ file }), {
      status: 200, headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(downloadStorageFile({ root: 'iso', directory: '', url: 'https://example.com/installer.iso' })).resolves.toEqual({
      name: 'installer.iso', path: 'installer.iso', kind: 'file', size_bytes: 5,
    })
    expect(fetchMock).toHaveBeenCalledWith('/twirp/apprunner.v1.AppRunnerService/DownloadStorageFile', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ root: 'iso', directory: '', url: 'https://example.com/installer.iso' }),
    }))
  })
})
