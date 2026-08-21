import { useCallback, useEffect, useState } from 'react'
import {
  Alert,
  Box,
  Button,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Divider,
  FormControlLabel,
  MenuItem,
  Stack,
  Switch,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TextField,
  Typography,
} from '@mui/material'
import {
  AddRounded,
  DeleteOutlineRounded,
  DesktopWindowsRounded,
  EjectRounded,
  PlayArrowRounded,
  PowerSettingsNewRounded,
  StopRounded,
  SettingsRounded,
} from '@mui/icons-material'
import { Link } from 'react-router-dom'

import {
  createVM,
  addVMCDROM,
  addVMDisk,
  configureVMIPMI,
  deleteVM,
  getNetworkingStatus,
  listISOs,
  listVMs,
  removeVMCDROM,
  removeVMDisk,
  startVM,
  stopVM,
  updateVM,
  updateVMCDROM,
  type CreateVMRequest,
  type ISOImage,
  type NetworkingStatus,
  type UpdateVMRequest,
  type VirtualMachine,
  type VMStatus,
  type VMIPMIConfiguration,
} from './api'

const defaultCreateRequest: CreateVMRequest = {
  name: '',
  cpus: 2,
  memory_mib: 2048,
  disk_gib: 20,
  iso_name: '',
  network_mode: 'NETWORK_MODE_NAT',
  bridge_name: '',
}

const dnsLabelPattern = /^(?=.{1,63}$)[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?$/

const statusDetails: Record<VMStatus, { label: string; color: 'default' | 'success' | 'warning' | 'error' }> = {
  VM_STATUS_UNSPECIFIED: { label: 'Unknown', color: 'default' },
  VM_STATUS_STOPPED: { label: 'Stopped', color: 'default' },
  VM_STATUS_RUNNING: { label: 'Running', color: 'success' },
  VM_STATUS_STOPPING: { label: 'Stopping', color: 'warning' },
  VM_STATUS_ERROR: { label: 'Error', color: 'error' },
}

export default function VirtualMachinesPage({ refreshInterval }: { refreshInterval: number | null }) {
  const [virtualMachines, setVirtualMachines] = useState<VirtualMachine[]>([])
  const [images, setImages] = useState<ISOImage[]>([])
  const [networking, setNetworking] = useState<NetworkingStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [busyID, setBusyID] = useState('')
  const [error, setError] = useState('')
  const [createOpen, setCreateOpen] = useState(false)
  const [settingsVM, setSettingsVM] = useState<VirtualMachine | null>(null)
  const [settingsRequest, setSettingsRequest] = useState<UpdateVMRequest | null>(null)
  const [ipmiRequest, setIPMIRequest] = useState<VMIPMIConfiguration | null>(null)
  const [newDiskGiB, setNewDiskGiB] = useState(20)
  const [newCDROMISO, setNewCDROMISO] = useState('')
  const [createRequest, setCreateRequest] = useState<CreateVMRequest>(defaultCreateRequest)
  const createName = createRequest.name.trim()
  const createNameValid = dnsLabelPattern.test(createName)
  const settingsName = settingsRequest?.name.trim() ?? ''
  const settingsNameValid = dnsLabelPattern.test(settingsName)
  const settingsRunning = settingsVM?.status === 'VM_STATUS_RUNNING'
  const settingsStopping = settingsVM?.status === 'VM_STATUS_STOPPING'
  const settingsBusy = busyID.startsWith('settings-')

  const refresh = useCallback(async (showLoading = false) => {
    if (showLoading) setLoading(true)
    try {
      const [vms, isos, networkStatus] = await Promise.all([listVMs(), listISOs(), getNetworkingStatus()])
      setVirtualMachines(vms)
      setImages(isos)
      setNetworking(networkStatus)
      const bridges = networkStatus.bridges ?? []
      const suggestedBridge = bridges.find((bridge) => bridge.usable_by_qemu)?.name ?? bridges[0]?.name ?? ''
      setCreateRequest((current) => ({
        ...current,
        iso_name: current.iso_name || isos[0]?.name || '',
        bridge_name: current.bridge_name || suggestedBridge,
      }))
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'Unable to load virtual machines')
    } finally {
      if (showLoading) setLoading(false)
    }
  }, [])

  useEffect(() => {
    void refresh(true)
    if (refreshInterval === null) return
    const timer = window.setInterval(() => void refresh(), refreshInterval)
    return () => window.clearInterval(timer)
  }, [refresh, refreshInterval])

  const runOperation = async (id: string, operation: () => Promise<unknown>) => {
    setBusyID(id)
    setError('')
    try {
      await operation()
      await refresh()
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'The operation failed')
    } finally {
      setBusyID('')
    }
  }

  const submitCreate = async () => {
    setBusyID('create')
    setError('')
    try {
      await createVM(createRequest)
      setCreateOpen(false)
      setCreateRequest({ ...defaultCreateRequest, iso_name: images[0]?.name ?? '' })
      await refresh()
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'Unable to create the virtual machine')
    } finally {
      setBusyID('')
    }
  }

  const removeVM = async (vm: VirtualMachine) => {
    if (!window.confirm(`Delete ${vm.name} and its system disk? This cannot be undone.`)) return
    await runOperation(vm.id, () => deleteVM(vm.id))
  }

  const openSettings = (vm: VirtualMachine) => {
    const bridges = networking?.bridges ?? []
    const bridgeName = vm.ipmi?.bridge_name || bridges.find((bridge) => bridge.dhcp?.enabled)?.name || bridges[0]?.name || ''
    setSettingsVM(vm)
    setSettingsRequest({ id: vm.id, name: vm.name, description: vm.description ?? '', cpus: vm.cpus, memory_mib: vm.memory_mib })
    setIPMIRequest({
      id: vm.id,
      enabled: vm.ipmi?.enabled ?? false,
      bridge_name: bridgeName,
      address: vm.ipmi?.address || suggestIPMIAddress(bridgeName, bridges, virtualMachines),
      username: vm.ipmi?.username || 'admin',
      password: '',
    })
    setNewDiskGiB(20)
    setNewCDROMISO(images[0]?.name ?? '')
  }

  const closeSettings = () => {
    setSettingsVM(null)
    setSettingsRequest(null)
    setIPMIRequest(null)
  }

  const submitSettings = async () => {
    if (!settingsRequest || !ipmiRequest) return
    setBusyID(`settings-${settingsRequest.id}`)
    setError('')
    try {
      await updateVM(settingsRequest)
      await configureVMIPMI(ipmiRequest)
      closeSettings()
      await refresh()
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'Unable to update virtual machine settings')
    } finally {
      setBusyID('')
    }
  }

  const runSettingsDeviceOperation = async (operation: () => Promise<VirtualMachine>) => {
    if (!settingsVM) return
    setBusyID(`settings-device-${settingsVM.id}`)
    setError('')
    try {
      const updated = await operation()
      setSettingsVM(updated)
      setVirtualMachines((current) => current.map((vm) => vm.id === updated.id ? updated : vm))
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'Unable to update virtual machine devices')
    } finally {
      setBusyID('')
    }
  }

  return (
    <Box>
      <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2} sx={{ justifyContent: 'space-between', mb: 3.5 }}>
        <Box>
          <Typography variant="h4">Virtual machines</Typography>
          <Typography color="text.secondary" sx={{ mt: 0.75 }}>
            Create and manage local QEMU/KVM workloads.
          </Typography>
        </Box>
        <Button
          variant="contained"
          startIcon={<AddRounded />}
          onClick={() => setCreateOpen(true)}
          disabled={images.length === 0 || loading}
          sx={{ alignSelf: { xs: 'stretch', sm: 'center' } }}
        >
          Create virtual machine
        </Button>
      </Stack>

      {error && (
        <Alert severity="error" onClose={() => setError('')} sx={{ mb: 2.5 }}>
          {error}
        </Alert>
      )}
      {!loading && images.length === 0 && (
        <Alert severity="info" sx={{ mb: 2.5 }}>
          Add an `.iso` file to the configured ISO directory before creating a virtual machine.
        </Alert>
      )}

      <TableContainer sx={{ border: 1, borderColor: 'divider', borderRadius: 2, bgcolor: 'background.paper' }}>
        <Table>
          <TableHead>
            <TableRow>
              <TableCell>Name</TableCell>
              <TableCell>Status</TableCell>
              <TableCell>Resources</TableCell>
              <TableCell>Network</TableCell>
              <TableCell>Installation media</TableCell>
              <TableCell align="right">Actions</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {virtualMachines.map((vm) => {
              const running = vm.status === 'VM_STATUS_RUNNING'
              const stopping = vm.status === 'VM_STATUS_STOPPING'
              const busy = busyID === vm.id
              const status = statusDetails[vm.status] ?? statusDetails.VM_STATUS_UNSPECIFIED
              return (
                <TableRow key={vm.id} hover>
                  <TableCell>
                    <Typography sx={{ fontWeight: 650 }}>{vm.name}</Typography>
                    {vm.description && <Typography variant="caption" color="text.secondary" sx={{ display: 'block' }}>{vm.description}</Typography>}
                    {vm.last_error && <Typography variant="caption" color="error">{vm.last_error}</Typography>}
                  </TableCell>
                  <TableCell><Chip size="small" label={status.label} color={status.color} /></TableCell>
                  <TableCell>{vm.cpus} vCPU · {formatMemory(vm.memory_mib)} · {(vm.disks ?? []).length || 1} disk{((vm.disks ?? []).length || 1) === 1 ? '' : 's'}</TableCell>
                  <TableCell>
                    <Typography variant="body2">{vm.network_mode === 'NETWORK_MODE_BRIDGE' ? `Bridge (${vm.bridge_name || 'missing'})` : 'NAT'}</Typography>
                    {vm.ipmi?.enabled && (
                      <Typography variant="caption" color={vm.ipmi.running ? 'success.main' : 'error.main'}>
                        IPMI {vm.ipmi.address}:{vm.ipmi.port || 623}{vm.ipmi.running ? '' : ` · ${vm.ipmi.last_error || 'not listening'}`}
                      </Typography>
                    )}
                  </TableCell>
                  <TableCell>{(vm.cdroms ?? []).map((cdrom) => cdrom.iso_name || 'Ejected').join(', ') || vm.iso_name || 'No CD-ROM'}</TableCell>
                  <TableCell align="right">
                    <Stack direction="row" spacing={0.5} sx={{ justifyContent: 'flex-end' }}>
                      <Button size="small" startIcon={<SettingsRounded />} disabled={busy} onClick={() => openSettings(vm)}>
                        Settings
                      </Button>
                      {running && (
                        <Button
                          component={Link}
                          to={`/compute/virtual-machines/${vm.id}/console`}
                          size="small"
                          startIcon={<DesktopWindowsRounded />}
                        >
                          Console
                        </Button>
                      )}
                      {!running && !stopping && (
                        <Button size="small" startIcon={<PlayArrowRounded />} disabled={busy} onClick={() => void runOperation(vm.id, () => startVM(vm.id))}>
                          Start
                        </Button>
                      )}
                      {running && (
                        <Button size="small" startIcon={<PowerSettingsNewRounded />} disabled={busy} onClick={() => void runOperation(vm.id, () => stopVM(vm.id))}>
                          Shut down
                        </Button>
                      )}
                      {(running || stopping) && (
                        <Button size="small" color="error" startIcon={<StopRounded />} disabled={busy} onClick={() => void runOperation(vm.id, () => stopVM(vm.id, true))}>
                          Force stop
                        </Button>
                      )}
                      {!running && !stopping && (
                        <Button size="small" color="error" startIcon={<DeleteOutlineRounded />} disabled={busy} onClick={() => void removeVM(vm)}>
                          Delete
                        </Button>
                      )}
                    </Stack>
                  </TableCell>
                </TableRow>
              )
            })}
            {!loading && virtualMachines.length === 0 && (
              <TableRow>
                <TableCell colSpan={6} align="center" sx={{ py: 8 }}>
                  <DesktopWindowsRounded color="disabled" sx={{ fontSize: 44 }} />
                  <Typography variant="h6" sx={{ mt: 1 }}>No virtual machines yet</Typography>
                  <Typography color="text.secondary">Select an ISO and create your first workload.</Typography>
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </TableContainer>

      <Dialog open={createOpen} onClose={() => setCreateOpen(false)} fullWidth maxWidth="sm">
        <DialogTitle>Create virtual machine</DialogTitle>
        <DialogContent>
          <Stack spacing={2.25} sx={{ mt: 1 }}>
            <TextField
              label="Name"
              autoFocus
              value={createRequest.name}
              onChange={(event) => setCreateRequest({ ...createRequest, name: event.target.value })}
              error={createName.length > 0 && !createNameValid}
              helperText="1–63 letters, numbers, or interior hyphens; this name is also used by Auto DNS"
            />
            <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2}>
              <TextField fullWidth type="number" label="vCPUs" slotProps={{ htmlInput: { min: 1, max: 64 } }} value={createRequest.cpus} onChange={(event) => setCreateRequest({ ...createRequest, cpus: Number(event.target.value) })} />
              <TextField fullWidth type="number" label="Memory (MiB)" slotProps={{ htmlInput: { min: 256, step: 256 } }} value={createRequest.memory_mib} onChange={(event) => setCreateRequest({ ...createRequest, memory_mib: Number(event.target.value) })} />
              <TextField fullWidth type="number" label="Disk (GiB)" slotProps={{ htmlInput: { min: 1 } }} value={createRequest.disk_gib} onChange={(event) => setCreateRequest({ ...createRequest, disk_gib: Number(event.target.value) })} />
            </Stack>
            <TextField select label="Installation ISO" value={createRequest.iso_name} onChange={(event) => setCreateRequest({ ...createRequest, iso_name: event.target.value })}>
              {images.map((image) => <MenuItem key={image.name} value={image.name}>{image.name} · {formatBytes(image.size_bytes)}</MenuItem>)}
            </TextField>
            <TextField
              select
              label="Network mode"
              value={createRequest.network_mode}
              onChange={(event) => setCreateRequest({ ...createRequest, network_mode: event.target.value as CreateVMRequest['network_mode'] })}
            >
              <MenuItem value="NETWORK_MODE_NAT">User-mode NAT</MenuItem>
              <MenuItem value="NETWORK_MODE_BRIDGE">Host bridge</MenuItem>
            </TextField>
            {createRequest.network_mode === 'NETWORK_MODE_BRIDGE' && (
              <TextField
                select
                label="Bridge"
                value={createRequest.bridge_name ?? ''}
                onChange={(event) => setCreateRequest({ ...createRequest, bridge_name: event.target.value })}
                helperText={
                  (networking?.bridges ?? []).length === 0
                    ? 'No Linux bridges were detected. Create one in Configuration → Networking.'
                    : !(networking?.bridges ?? []).find((bridge) => bridge.name === createRequest.bridge_name)?.usable_by_qemu
                      ? 'This bridge has failed diagnostics. Review Configuration → Networking before starting the VM.'
                      : undefined
                }
              >
                {(networking?.bridges ?? []).map((bridge) => (
                  <MenuItem key={bridge.name} value={bridge.name}>
                    {bridge.name}{bridge.usable_by_qemu ? '' : ' · diagnostics required'}
                  </MenuItem>
                ))}
              </TextField>
            )}
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setCreateOpen(false)}>Cancel</Button>
          <Button
            variant="contained"
            onClick={() => void submitCreate()}
            disabled={busyID === 'create' || !createNameValid || !createRequest.iso_name || (createRequest.network_mode === 'NETWORK_MODE_BRIDGE' && !createRequest.bridge_name)}
          >
            {busyID === 'create' ? 'Creating…' : 'Create'}
          </Button>
        </DialogActions>
      </Dialog>

      <Dialog open={settingsVM !== null} onClose={settingsBusy ? undefined : closeSettings} fullWidth maxWidth="md">
        <DialogTitle>
          <Stack direction="row" spacing={1} sx={{ alignItems: 'center' }}>
            <Typography variant="h6">Settings · {settingsVM?.name}</Typography>
            {settingsVM && <Chip size="small" color={settingsRunning ? 'success' : settingsStopping ? 'warning' : 'default'} label={statusDetails[settingsVM.status]?.label ?? 'Unknown'} />}
          </Stack>
        </DialogTitle>
        <DialogContent>
          {settingsVM && settingsRequest && ipmiRequest && (
            <Stack spacing={3} sx={{ mt: 1 }}>
              {(settingsRunning || settingsStopping) && (
                <Alert severity={settingsRunning ? 'info' : 'warning'}>
                  {settingsRunning
                    ? 'This VM is running. Name and description changes are immediate; CPU and memory changes apply on its next start. CD-ROM media can be changed or ejected live.'
                    : 'This VM is stopping. Wait for it to stop before changing device configuration.'}
                </Alert>
              )}

              <Box>
                <Typography variant="h6" sx={{ mb: 1.5 }}>General</Typography>
                <Stack spacing={2}>
                  <TextField
                    label="Name"
                    value={settingsRequest.name}
                    onChange={(event) => setSettingsRequest({ ...settingsRequest, name: event.target.value })}
                    error={settingsName.length > 0 && !settingsNameValid}
                    helperText="Used as the VM display name and Auto DNS hostname"
                  />
                  <TextField
                    label="Description"
                    multiline
                    minRows={2}
                    value={settingsRequest.description}
                    onChange={(event) => setSettingsRequest({ ...settingsRequest, description: event.target.value })}
                    slotProps={{ htmlInput: { maxLength: 4096 } }}
                  />
                  <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2}>
                    <TextField
                      fullWidth
                      type="number"
                      label="vCPUs"
                      value={settingsRequest.cpus}
                      onChange={(event) => setSettingsRequest({ ...settingsRequest, cpus: Number(event.target.value) })}
                      helperText={settingsRunning ? 'Applies on next start' : undefined}
                      slotProps={{ htmlInput: { min: 1, max: 64 } }}
                    />
                    <TextField
                      fullWidth
                      type="number"
                      label="Memory (MiB)"
                      value={settingsRequest.memory_mib}
                      onChange={(event) => setSettingsRequest({ ...settingsRequest, memory_mib: Number(event.target.value) })}
                      helperText={settingsRunning ? 'Applies on next start' : undefined}
                      slotProps={{ htmlInput: { min: 256, max: 1048576, step: 256 } }}
                    />
                  </Stack>
                </Stack>
              </Box>

              <Divider />
              <Box>
                <Typography variant="h6">Disks</Typography>
                <Typography variant="body2" color="text.secondary" sx={{ mb: 1.5 }}>
                  Disk devices can only be added or removed while the VM is stopped.
                </Typography>
                <Stack spacing={1}>
                  {(settingsVM.disks ?? []).map((disk) => (
                    <Stack key={disk.id} direction="row" spacing={1} sx={{ alignItems: 'center', justifyContent: 'space-between', border: 1, borderColor: 'divider', borderRadius: 1.5, p: 1.25 }}>
                      <Box>
                        <Typography variant="body2" sx={{ fontWeight: 650 }}>{disk.system ? 'System disk' : 'Data disk'}</Typography>
                        <Typography variant="caption" color="text.secondary">{disk.size_gib} GiB · qcow2</Typography>
                      </Box>
                      {!disk.system && (
                        <Button
                          size="small"
                          color="error"
                          startIcon={<DeleteOutlineRounded />}
                          disabled={settingsRunning || settingsStopping || settingsBusy}
                          onClick={() => {
                            if (window.confirm(`Remove this ${disk.size_gib} GiB disk? Its data will be permanently deleted.`)) {
                              void runSettingsDeviceOperation(() => removeVMDisk(settingsVM.id, disk.id))
                            }
                          }}
                        >
                          Remove
                        </Button>
                      )}
                    </Stack>
                  ))}
                  <Stack direction="row" spacing={1} sx={{ alignItems: 'flex-start' }}>
                    <TextField
                      size="small"
                      type="number"
                      label="New disk size (GiB)"
                      value={newDiskGiB}
                      onChange={(event) => setNewDiskGiB(Number(event.target.value))}
                      slotProps={{ htmlInput: { min: 1, max: 2048 } }}
                    />
                    <Button
                      variant="outlined"
                      startIcon={<AddRounded />}
                      disabled={settingsRunning || settingsStopping || settingsBusy || newDiskGiB < 1 || newDiskGiB > 2048}
                      onClick={() => void runSettingsDeviceOperation(() => addVMDisk(settingsVM.id, newDiskGiB))}
                    >
                      Add disk
                    </Button>
                  </Stack>
                </Stack>
              </Box>

              <Divider />
              <Box>
                <Typography variant="h6">CD-ROM devices</Typography>
                <Typography variant="body2" color="text.secondary" sx={{ mb: 1.5 }}>
                  ISO media changes and ejects apply immediately. Adding or removing a CD-ROM device requires the VM to be stopped.
                </Typography>
                <Stack spacing={1}>
                  {(settingsVM.cdroms ?? []).map((cdrom, index) => (
                    <Stack key={cdrom.id} direction={{ xs: 'column', sm: 'row' }} spacing={1} sx={{ alignItems: { sm: 'center' }, border: 1, borderColor: 'divider', borderRadius: 1.5, p: 1.25 }}>
                      <Typography variant="body2" sx={{ fontWeight: 650, minWidth: 90 }}>CD-ROM {index + 1}</Typography>
                      <TextField
                        select
                        fullWidth
                        size="small"
                        label="ISO media"
                        value={cdrom.iso_name ?? ''}
                        disabled={settingsStopping || settingsBusy}
                        onChange={(event) => void runSettingsDeviceOperation(() => updateVMCDROM(settingsVM.id, cdrom.id, event.target.value))}
                      >
                        <MenuItem value="">Empty</MenuItem>
                        {images.map((image) => <MenuItem key={image.name} value={image.name}>{image.name} · {formatBytes(image.size_bytes)}</MenuItem>)}
                      </TextField>
                      <Button
                        size="small"
                        startIcon={<EjectRounded />}
                        disabled={!cdrom.iso_name || settingsStopping || settingsBusy}
                        onClick={() => void runSettingsDeviceOperation(() => updateVMCDROM(settingsVM.id, cdrom.id, ''))}
                      >
                        Eject
                      </Button>
                      <Button
                        size="small"
                        color="error"
                        disabled={settingsRunning || settingsStopping || settingsBusy}
                        onClick={() => void runSettingsDeviceOperation(() => removeVMCDROM(settingsVM.id, cdrom.id))}
                      >
                        Remove device
                      </Button>
                    </Stack>
                  ))}
                  <Stack direction={{ xs: 'column', sm: 'row' }} spacing={1}>
                    <TextField select size="small" label="Initial ISO media" value={newCDROMISO} onChange={(event) => setNewCDROMISO(event.target.value)} sx={{ minWidth: 260 }}>
                      <MenuItem value="">Empty</MenuItem>
                      {images.map((image) => <MenuItem key={image.name} value={image.name}>{image.name}</MenuItem>)}
                    </TextField>
                    <Button
                      variant="outlined"
                      startIcon={<AddRounded />}
                      disabled={settingsRunning || settingsStopping || settingsBusy}
                      onClick={() => void runSettingsDeviceOperation(() => addVMCDROM(settingsVM.id, newCDROMISO))}
                    >
                      Add CD-ROM
                    </Button>
                  </Stack>
                </Stack>
              </Box>

              <Divider />
              <Box>
                <Typography variant="h6" sx={{ mb: 1 }}>IPMI</Typography>
                <FormControlLabel
                  control={<Switch checked={ipmiRequest.enabled} onChange={(event) => setIPMIRequest({ ...ipmiRequest, enabled: event.target.checked })} />}
                  label="Enable IPMI"
                />
                {ipmiRequest.enabled && (
                  <Stack spacing={2} sx={{ mt: 1.5 }}>
                    <TextField
                      select
                      label="Management bridge"
                      value={ipmiRequest.bridge_name}
                      onChange={(event) => {
                        const bridgeName = event.target.value
                        setIPMIRequest({ ...ipmiRequest, bridge_name: bridgeName, address: suggestIPMIAddress(bridgeName, networking?.bridges ?? [], virtualMachines, settingsVM.id) })
                      }}
                    >
                      {(networking?.bridges ?? []).map((bridge) => <MenuItem key={bridge.name} value={bridge.name}>{bridge.name}{bridge.dhcp?.enabled ? ' · managed subnet' : ''}</MenuItem>)}
                    </TextField>
                    <TextField
                      label="Management IPv4 address"
                      value={ipmiRequest.address}
                      onChange={(event) => setIPMIRequest({ ...ipmiRequest, address: event.target.value })}
                      helperText="A unique address on this bridge; App Runner assigns it while IPMI is enabled."
                    />
                    <TextField label="Username" value={ipmiRequest.username} onChange={(event) => setIPMIRequest({ ...ipmiRequest, username: event.target.value })} slotProps={{ htmlInput: { maxLength: 16 } }} />
                    <TextField
                      label="Password"
                      type="password"
                      value={ipmiRequest.password}
                      onChange={(event) => setIPMIRequest({ ...ipmiRequest, password: event.target.value })}
                      helperText={settingsVM.ipmi?.enabled ? 'Leave blank to retain the existing password.' : 'Required; IPMI passwords are limited to 20 bytes.'}
                      slotProps={{ htmlInput: { maxLength: 20 } }}
                    />
                    <Alert severity="warning">Expose IPMI only on a trusted management network. The listener uses IPMI 2.0 lanplus on UDP port 623.</Alert>
                  </Stack>
                )}
              </Box>
            </Stack>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={closeSettings} disabled={settingsBusy}>Cancel</Button>
          <Button
            variant="contained"
            onClick={() => void submitSettings()}
            disabled={
              !settingsRequest || !ipmiRequest || settingsBusy || !settingsNameValid ||
              settingsRequest.cpus < 1 || settingsRequest.cpus > 64 || settingsRequest.memory_mib < 256 || settingsRequest.memory_mib > 1048576 ||
              (ipmiRequest.enabled && (!ipmiRequest.bridge_name || !ipmiRequest.address || !ipmiRequest.username || (!settingsVM?.ipmi?.enabled && !ipmiRequest.password)))
            }
          >
            {settingsBusy ? 'Saving…' : 'Save settings'}
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  )
}

function suggestIPMIAddress(bridgeName: string, bridges: NetworkingStatus['bridges'], vms: VirtualMachine[], excludeVMID = ''): string {
  const bridge = (bridges ?? []).find((candidate) => candidate.name === bridgeName)
  const cidr = bridge?.dhcp?.enabled ? bridge.dhcp.cidr : bridge?.addresses?.find((address) => address.includes('.'))
  if (!cidr) return ''
  const [address, bitsText] = cidr.split('/')
  const bits = Number(bitsText)
  const octets = address.split('.').map(Number)
  if (octets.length !== 4 || octets.some((value) => !Number.isInteger(value) || value < 0 || value > 255) || bits !== 24) return ''
  const used = new Set(vms.filter((vm) => vm.id !== excludeVMID && vm.ipmi?.enabled).map((vm) => vm.ipmi?.address))
  for (let host = 2; host < 50; host += 1) {
    const candidate = `${octets[0]}.${octets[1]}.${octets[2]}.${host}`
    if (!used.has(candidate) && candidate !== bridge?.dhcp?.server_address) return candidate
  }
  return ''
}

function formatMemory(memoryMiB: number): string {
  return memoryMiB >= 1024 ? `${memoryMiB / 1024} GiB` : `${memoryMiB} MiB`
}

function formatBytes(value: string): string {
  const bytes = Number(value)
  if (!Number.isFinite(bytes)) return value
  if (bytes >= 1024 ** 3) return `${(bytes / 1024 ** 3).toFixed(1)} GiB`
  if (bytes >= 1024 ** 2) return `${(bytes / 1024 ** 2).toFixed(1)} MiB`
  return `${bytes} bytes`
}
