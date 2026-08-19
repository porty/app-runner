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
  MenuItem,
  Stack,
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
  PlayArrowRounded,
  PowerSettingsNewRounded,
  StopRounded,
} from '@mui/icons-material'
import { useNavigate } from 'react-router-dom'

import {
  createVM,
  deleteVM,
  getHostStatus,
  listISOs,
  listVMs,
  startVM,
  stopVM,
  type CreateVMRequest,
  type HostStatus,
  type ISOImage,
  type VirtualMachine,
  type VMStatus,
} from './api'

const defaultCreateRequest: CreateVMRequest = {
  name: '',
  cpus: 2,
  memory_mib: 2048,
  disk_gib: 20,
  iso_name: '',
  network_mode: 'NETWORK_MODE_NAT',
}

const statusDetails: Record<VMStatus, { label: string; color: 'default' | 'success' | 'warning' | 'error' }> = {
  VM_STATUS_UNSPECIFIED: { label: 'Unknown', color: 'default' },
  VM_STATUS_STOPPED: { label: 'Stopped', color: 'default' },
  VM_STATUS_RUNNING: { label: 'Running', color: 'success' },
  VM_STATUS_STOPPING: { label: 'Stopping', color: 'warning' },
  VM_STATUS_ERROR: { label: 'Error', color: 'error' },
}

export default function VirtualMachinesPage() {
  const navigate = useNavigate()
  const [virtualMachines, setVirtualMachines] = useState<VirtualMachine[]>([])
  const [images, setImages] = useState<ISOImage[]>([])
  const [hostStatus, setHostStatus] = useState<HostStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [busyID, setBusyID] = useState('')
  const [error, setError] = useState('')
  const [createOpen, setCreateOpen] = useState(false)
  const [createRequest, setCreateRequest] = useState<CreateVMRequest>(defaultCreateRequest)

  const refresh = useCallback(async (showLoading = false) => {
    if (showLoading) setLoading(true)
    try {
      const [vms, isos, status] = await Promise.all([listVMs(), listISOs(), getHostStatus()])
      setVirtualMachines(vms)
      setImages(isos)
      setHostStatus(status)
      setCreateRequest((current) => ({ ...current, iso_name: current.iso_name || isos[0]?.name || '' }))
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'Unable to load virtual machines')
    } finally {
      if (showLoading) setLoading(false)
    }
  }, [])

  useEffect(() => {
    void refresh(true)
    const timer = window.setInterval(() => void refresh(), 3000)
    return () => window.clearInterval(timer)
  }, [refresh])

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
                    {vm.last_error && <Typography variant="caption" color="error">{vm.last_error}</Typography>}
                  </TableCell>
                  <TableCell><Chip size="small" label={status.label} color={status.color} /></TableCell>
                  <TableCell>{vm.cpus} vCPU · {formatMemory(vm.memory_mib)} · {vm.disk_gib} GiB</TableCell>
                  <TableCell>{vm.network_mode === 'NETWORK_MODE_BRIDGE' ? `Bridge (${hostStatus?.bridge_name ?? 'br0'})` : 'NAT'}</TableCell>
                  <TableCell>{vm.iso_name}</TableCell>
                  <TableCell align="right">
                    <Stack direction="row" spacing={0.5} sx={{ justifyContent: 'flex-end' }}>
                      {running && (
                        <Button size="small" startIcon={<DesktopWindowsRounded />} onClick={() => navigate(`/compute/virtual-machines/${vm.id}/console`)}>
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
            <TextField label="Name" autoFocus value={createRequest.name} onChange={(event) => setCreateRequest({ ...createRequest, name: event.target.value })} />
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
              helperText={createRequest.network_mode === 'NETWORK_MODE_BRIDGE' && !hostStatus?.bridge_available ? hostStatus?.bridge_warning : undefined}
            >
              <MenuItem value="NETWORK_MODE_NAT">User-mode NAT</MenuItem>
              <MenuItem value="NETWORK_MODE_BRIDGE">Host bridge ({hostStatus?.bridge_name ?? 'br0'})</MenuItem>
            </TextField>
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setCreateOpen(false)}>Cancel</Button>
          <Button variant="contained" onClick={() => void submitCreate()} disabled={busyID === 'create' || !createRequest.name.trim() || !createRequest.iso_name}>
            {busyID === 'create' ? 'Creating…' : 'Create'}
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  )
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
