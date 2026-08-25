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
  IconButton,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material'
import {
  AddRounded,
  ContentCopyRounded,
  DeleteOutlineRounded,
  DesktopWindowsRounded,
  PlayArrowRounded,
  PowerSettingsNewRounded,
  SettingsRounded,
  StopRounded,
} from '@mui/icons-material'
import { Link } from 'react-router-dom'

import { cloneVM, deleteVM, getNetworkingStatus, listVMs, startVM, stopVM, type NetworkingStatus, type VirtualMachine, type VMStatus } from './api'
import { canAccessVMConsole } from './vmConsoleAccess'
import { copyText } from './clipboard'

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
  const [networking, setNetworking] = useState<NetworkingStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [busyID, setBusyID] = useState('')
  const [error, setError] = useState('')
  const [cloneSource, setCloneSource] = useState<VirtualMachine | null>(null)
  const [cloneName, setCloneName] = useState('')
  const trimmedCloneName = cloneName.trim()
  const cloneNameExists = virtualMachines.some((vm) => vm.name.toLowerCase() === trimmedCloneName.toLowerCase())
  const cloneNameValid = dnsLabelPattern.test(trimmedCloneName) && !cloneNameExists
  const cloneBusy = cloneSource !== null && busyID === cloneSource.id

  const refresh = useCallback(async (showLoading = false) => {
    if (showLoading) setLoading(true)
    try {
      const [machines, networkingStatus] = await Promise.all([listVMs(), getNetworkingStatus().catch(() => null)])
      setVirtualMachines(machines)
      setNetworking(networkingStatus)
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

  const removeVM = async (vm: VirtualMachine) => {
    if (!window.confirm(`Delete ${vm.name} and its disks? This cannot be undone.`)) return
    await runOperation(vm.id, () => deleteVM(vm.id))
  }

  const openClone = (vm: VirtualMachine) => {
    setCloneSource(vm)
    setCloneName(suggestCloneName(vm.name, virtualMachines))
    setError('')
  }

  const submitClone = async () => {
    if (!cloneSource) return
    setBusyID(cloneSource.id)
    setError('')
    try {
      await cloneVM(cloneSource.id, trimmedCloneName)
      setCloneSource(null)
      setCloneName('')
      await refresh()
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'Unable to clone the virtual machine')
    } finally {
      setBusyID('')
    }
  }

  const copyIPAddress = async (address: string) => {
    try {
      await copyText(address)
    } catch (copyError) {
      setError(copyError instanceof Error ? `Unable to copy IP address: ${copyError.message}` : 'Unable to copy IP address')
    }
  }

  const addressesByMAC = new Map<string, string>()
  for (const bridge of networking?.bridges ?? []) {
    for (const lease of bridge.dhcp?.leases ?? []) {
      addressesByMAC.set(lease.hardware_address.toLowerCase(), lease.address)
    }
  }

  return (
    <Box>
      <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2} sx={{ justifyContent: 'space-between', mb: 3.5 }}>
        <Box>
          <Typography variant="h4">Virtual machines</Typography>
          <Typography color="text.secondary" sx={{ mt: 0.75 }}>Create and manage local QEMU/KVM workloads.</Typography>
        </Box>
        <Button component={Link} to="/compute/virtual-machines/new" variant="contained" startIcon={<AddRounded />} sx={{ alignSelf: { xs: 'stretch', sm: 'center' } }}>
          Create virtual machine
        </Button>
      </Stack>

      {error && <Alert severity="error" onClose={() => setError('')} sx={{ mb: 2.5 }}>{error}</Alert>}

      <TableContainer sx={{ border: 1, borderColor: 'divider', borderRadius: 2, bgcolor: 'background.paper' }}>
        <Table>
          <TableHead><TableRow><TableCell>Name</TableCell><TableCell>Status</TableCell><TableCell>Resources</TableCell><TableCell>Network</TableCell><TableCell>Boot source</TableCell><TableCell align="right">Actions</TableCell></TableRow></TableHead>
          <TableBody>
            {virtualMachines.map((vm) => {
              const running = vm.status === 'VM_STATUS_RUNNING'
              const stopping = vm.status === 'VM_STATUS_STOPPING'
              const busy = busyID === vm.id
              const status = statusDetails[vm.status] ?? statusDetails.VM_STATUS_UNSPECIFIED
              const ipAddress = addressesByMAC.get(vm.mac_address.toLowerCase())
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
                    <Typography variant="caption" color="text.secondary" sx={{ display: 'block' }}>{vm.mac_address}</Typography>
                    {ipAddress && <Stack direction="row" spacing={0.25} sx={{ alignItems: 'center', mt: 0.25 }}>
                      <Typography variant="caption" sx={{ fontFamily: 'monospace', fontVariantNumeric: 'tabular-nums', width: '15ch', whiteSpace: 'nowrap' }}>{ipAddress}</Typography>
                      <Tooltip title="Copy IP address"><IconButton size="small" aria-label={`Copy IP address ${ipAddress}`} onClick={() => void copyIPAddress(ipAddress)}><ContentCopyRounded fontSize="inherit" /></IconButton></Tooltip>
                    </Stack>}
                    {vm.ipmi?.enabled && <Typography variant="caption" sx={{ display: 'block' }} color={vm.ipmi.running ? 'success.main' : 'error.main'}>IPMI {vm.ipmi.address}:{vm.ipmi.port || 623}{vm.ipmi.running ? '' : ` · ${vm.ipmi.last_error || 'not listening'}`}</Typography>}
                  </TableCell>
                  <TableCell>{vm.source_image ? `Cloud image: ${vm.source_image}` : (vm.cdroms ?? []).map((cdrom) => cdrom.iso_name || 'Ejected').join(', ') || vm.iso_name || 'No CD-ROM'}</TableCell>
                  <TableCell align="right">
                    <Stack direction="row" spacing={0.5} sx={{ justifyContent: 'flex-end' }}>
                      <Button component={Link} to={`/compute/virtual-machines/${vm.id}/settings`} size="small" startIcon={<SettingsRounded />} disabled={busy}>Settings</Button>
                      {canAccessVMConsole(vm.status) && <Button component={Link} to={`/compute/virtual-machines/${vm.id}/console`} size="small" startIcon={<DesktopWindowsRounded />}>Console</Button>}
                      {vm.status === 'VM_STATUS_STOPPED' && <Button size="small" startIcon={<ContentCopyRounded />} disabled={busy} onClick={() => openClone(vm)}>Clone</Button>}
                      {!running && !stopping && <Button size="small" startIcon={<PlayArrowRounded />} disabled={busy} onClick={() => void runOperation(vm.id, () => startVM(vm.id))}>Start</Button>}
                      {running && <Button size="small" startIcon={<PowerSettingsNewRounded />} disabled={busy} onClick={() => void runOperation(vm.id, () => stopVM(vm.id))}>Shut down</Button>}
                      {(running || stopping) && <Button size="small" color="error" startIcon={<StopRounded />} disabled={busy} onClick={() => void runOperation(vm.id, () => stopVM(vm.id, true))}>Force stop</Button>}
                      {!running && !stopping && <Button size="small" color="error" startIcon={<DeleteOutlineRounded />} disabled={busy} onClick={() => void removeVM(vm)}>Delete</Button>}
                    </Stack>
                  </TableCell>
                </TableRow>
              )
            })}
            {!loading && virtualMachines.length === 0 && <TableRow><TableCell colSpan={6} align="center" sx={{ py: 8 }}><DesktopWindowsRounded color="disabled" sx={{ fontSize: 44 }} /><Typography variant="h6" sx={{ mt: 1 }}>No virtual machines yet</Typography><Typography color="text.secondary">Create a VM from an installer or cloud image.</Typography></TableCell></TableRow>}
          </TableBody>
        </Table>
      </TableContainer>

      <Dialog open={Boolean(cloneSource)} onClose={cloneBusy ? undefined : () => setCloneSource(null)} fullWidth maxWidth="sm">
        <DialogTitle>Clone {cloneSource?.name}</DialogTitle>
        <DialogContent>
          <Stack spacing={2.25} sx={{ mt: 1 }}>
            <Alert severity="info">
              All virtual disks will be fully copied. The clone receives a new MAC address and starts with IPMI disabled. Guest hostnames, machine IDs, SSH keys, and static IP settings remain unchanged inside the copied disks.
            </Alert>
            <TextField
              label="New virtual machine name"
              autoFocus
              value={cloneName}
              onChange={(event) => setCloneName(event.target.value)}
              error={trimmedCloneName.length > 0 && !cloneNameValid}
              helperText={cloneNameExists ? 'A virtual machine with this name already exists.' : '1–63 letters, numbers, or interior hyphens; this name is also used by Auto DNS'}
            />
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setCloneSource(null)} disabled={cloneBusy}>Cancel</Button>
          <Button variant="contained" onClick={() => void submitClone()} disabled={cloneBusy || !cloneNameValid}>{cloneBusy ? 'Cloning…' : 'Clone'}</Button>
        </DialogActions>
      </Dialog>
    </Box>
  )
}

function suggestCloneName(sourceName: string, virtualMachines: VirtualMachine[]): string {
  const names = new Set(virtualMachines.map((vm) => vm.name.toLowerCase()))
  for (let suffix = 1; suffix < 1000; suffix += 1) {
    const ending = suffix === 1 ? '-copy' : `-copy-${suffix}`
    const candidate = `${sourceName.slice(0, 63 - ending.length)}${ending}`
    if (!names.has(candidate.toLowerCase())) return candidate
  }
  return ''
}

function formatMemory(memoryMiB: number): string {
  return memoryMiB >= 1024 ? `${memoryMiB / 1024} GiB` : `${memoryMiB} MiB`
}
