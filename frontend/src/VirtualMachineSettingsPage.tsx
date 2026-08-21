import { useCallback, useEffect, useState } from 'react'
import {
  Alert,
  Box,
  Button,
  Chip,
  CircularProgress,
  Divider,
  FormControlLabel,
  MenuItem,
  Stack,
  Switch,
  Tab,
  Tabs,
  TextField,
  Typography,
} from '@mui/material'
import {
  AddRounded,
  ArrowBackRounded,
  DeleteOutlineRounded,
  EjectRounded,
  SaveRounded,
} from '@mui/icons-material'
import { Link, useParams } from 'react-router-dom'

import {
  addVMCDROM,
  addVMDisk,
  configureVMIPMI,
  getNetworkingStatus,
  getVM,
  listISOs,
  listVMs,
  removeVMCDROM,
  removeVMDisk,
  updateVM,
  updateVMCDROM,
  updateVMNetwork,
  type ISOImage,
  type NetworkingStatus,
  type UpdateVMNetworkRequest,
  type UpdateVMRequest,
  type VirtualMachine,
  type VMIPMIConfiguration,
} from './api'

const dnsLabelPattern = /^(?=.{1,63}$)[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?$/
const macAddressPattern = /^(?:[0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}$/

type SettingsTab = 'general' | 'storage' | 'networking'

export default function VirtualMachineSettingsPage() {
  const { id = '' } = useParams()
  const [vm, setVM] = useState<VirtualMachine | null>(null)
  const [images, setImages] = useState<ISOImage[]>([])
  const [networking, setNetworking] = useState<NetworkingStatus | null>(null)
  const [virtualMachines, setVirtualMachines] = useState<VirtualMachine[]>([])
  const [general, setGeneral] = useState<UpdateVMRequest | null>(null)
  const [network, setNetwork] = useState<UpdateVMNetworkRequest | null>(null)
  const [ipmi, setIPMI] = useState<VMIPMIConfiguration | null>(null)
  const [tab, setTab] = useState<SettingsTab>('general')
  const [newDiskGiB, setNewDiskGiB] = useState(20)
  const [newCDROMISO, setNewCDROMISO] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [loadedVM, loadedImages, loadedNetworking, loadedVMs] = await Promise.all([
        getVM(id), listISOs(), getNetworkingStatus(), listVMs(),
      ])
      const bridges = loadedNetworking.bridges ?? []
      const ipmiBridge = loadedVM.ipmi?.bridge_name || bridges.find((bridge) => bridge.dhcp?.enabled)?.name || bridges[0]?.name || ''
      setVM(loadedVM)
      setImages(loadedImages)
      setNetworking(loadedNetworking)
      setVirtualMachines(loadedVMs)
      setGeneral({
        id: loadedVM.id,
        name: loadedVM.name,
        description: loadedVM.description ?? '',
        cpus: loadedVM.cpus,
        memory_mib: loadedVM.memory_mib,
      })
      setNetwork({
        id: loadedVM.id,
        network_mode: loadedVM.network_mode,
        bridge_name: loadedVM.bridge_name ?? '',
        mac_address: loadedVM.mac_address,
      })
      setIPMI({
        id: loadedVM.id,
        enabled: loadedVM.ipmi?.enabled ?? false,
        bridge_name: ipmiBridge,
        address: loadedVM.ipmi?.address || suggestIPMIAddress(ipmiBridge, bridges, loadedVMs, loadedVM.id),
        username: loadedVM.ipmi?.username || 'admin',
        password: '',
      })
      setNewCDROMISO(loadedImages[0]?.name ?? '')
      setError('')
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'Unable to load virtual machine settings')
    } finally {
      setLoading(false)
    }
  }, [id])

  useEffect(() => { void load() }, [load])

  const runDeviceOperation = async (operation: () => Promise<VirtualMachine>) => {
    setBusy(true)
    setError('')
    try {
      const updated = await operation()
      setVM(updated)
      setVirtualMachines((current) => current.map((candidate) => candidate.id === updated.id ? updated : candidate))
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'Unable to update virtual machine devices')
    } finally {
      setBusy(false)
    }
  }

  const save = async () => {
    if (!general || !network || !ipmi) return
    setBusy(true)
    setError('')
    try {
      await updateVM(general)
      await updateVMNetwork(network)
      await configureVMIPMI(ipmi)
      const updated = await getVM(id)
      setVM(updated)
      setGeneral({ ...general, name: updated.name, description: updated.description ?? '', cpus: updated.cpus, memory_mib: updated.memory_mib })
      setNetwork({ ...network, network_mode: updated.network_mode, bridge_name: updated.bridge_name ?? '', mac_address: updated.mac_address })
      setIPMI({
        ...ipmi,
        enabled: updated.ipmi?.enabled ?? false,
        bridge_name: updated.ipmi?.bridge_name ?? ipmi.bridge_name,
        address: updated.ipmi?.address ?? ipmi.address,
        username: updated.ipmi?.username ?? ipmi.username,
        password: '',
      })
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'Unable to save virtual machine settings')
    } finally {
      setBusy(false)
    }
  }

  if (loading) return <Box sx={{ display: 'grid', placeItems: 'center', minHeight: 320 }}><CircularProgress /></Box>
  if (!vm || !general || !network || !ipmi) return <Alert severity="error">{error || 'Virtual machine settings are unavailable.'}</Alert>

  const running = vm.status === 'VM_STATUS_RUNNING'
  const stopping = vm.status === 'VM_STATUS_STOPPING'
  const active = running || stopping
  const nameValid = dnsLabelPattern.test(general.name.trim())
  const macValid = macAddressPattern.test(network.mac_address.trim()) && (Number.parseInt(network.mac_address.slice(0, 2), 16) & 1) === 0
  const ipmiValid = !ipmi.enabled || Boolean(ipmi.bridge_name && ipmi.address && ipmi.username && (vm.ipmi?.enabled || ipmi.password))
  const saveDisabled = busy || !nameValid || general.cpus < 1 || general.cpus > 64 || general.memory_mib < 256 || general.memory_mib > 1048576 ||
    !macValid || (network.network_mode === 'NETWORK_MODE_BRIDGE' && !network.bridge_name) || !ipmiValid

  return (
    <Box>
      <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2} sx={{ justifyContent: 'space-between', alignItems: { sm: 'center' }, mb: 3 }}>
        <Box>
          <Button component={Link} to="/compute/virtual-machines" startIcon={<ArrowBackRounded />} sx={{ mb: 1 }}>Virtual machines</Button>
          <Stack direction="row" spacing={1} sx={{ alignItems: 'center' }}>
            <Typography variant="h4">{vm.name} settings</Typography>
            <Chip size="small" color={running ? 'success' : stopping ? 'warning' : 'default'} label={running ? 'Running' : stopping ? 'Stopping' : 'Stopped'} />
          </Stack>
        </Box>
        <Button variant="contained" startIcon={<SaveRounded />} disabled={saveDisabled} onClick={() => void save()}>
          {busy ? 'Saving…' : 'Save settings'}
        </Button>
      </Stack>

      {error && <Alert severity="error" onClose={() => setError('')} sx={{ mb: 2.5 }}>{error}</Alert>}
      {(running || stopping) && (
        <Alert severity={running ? 'info' : 'warning'} sx={{ mb: 2.5 }}>
          {running
            ? 'This VM is running. Name, description, and IPMI changes are immediate; CPU and memory changes apply on its next start. ISO media can be changed or ejected live.'
            : 'This VM is stopping. Wait for it to stop before changing device or virtual NIC configuration.'}
        </Alert>
      )}

      <Box sx={{ border: 1, borderColor: 'divider', borderRadius: 2, bgcolor: 'background.paper', overflow: 'hidden' }}>
        <Tabs value={tab} onChange={(_event, value: SettingsTab) => setTab(value)} variant="scrollable" scrollButtons="auto" sx={{ px: 2, borderBottom: 1, borderColor: 'divider' }}>
          <Tab value="general" label="General" />
          <Tab value="storage" label="Disks / CD-ROM" />
          <Tab value="networking" label="Networking" />
        </Tabs>

        <Box sx={{ p: { xs: 2, md: 3 } }}>
          {tab === 'general' && (
            <Stack spacing={2.25} sx={{ maxWidth: 760 }}>
              <TextField
                label="Name"
                value={general.name}
                onChange={(event) => setGeneral({ ...general, name: event.target.value })}
                error={general.name.length > 0 && !nameValid}
                helperText="Used as the VM display name and Auto DNS hostname"
              />
              <TextField label="Description" multiline minRows={3} value={general.description} onChange={(event) => setGeneral({ ...general, description: event.target.value })} slotProps={{ htmlInput: { maxLength: 4096 } }} />
              <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2}>
                <TextField fullWidth type="number" label="vCPUs" value={general.cpus} onChange={(event) => setGeneral({ ...general, cpus: Number(event.target.value) })} helperText={running ? 'Applies on next start' : undefined} slotProps={{ htmlInput: { min: 1, max: 64 } }} />
                <TextField fullWidth type="number" label="Memory (MiB)" value={general.memory_mib} onChange={(event) => setGeneral({ ...general, memory_mib: Number(event.target.value) })} helperText={running ? 'Applies on next start' : undefined} slotProps={{ htmlInput: { min: 256, max: 1048576, step: 256 } }} />
              </Stack>
            </Stack>
          )}

          {tab === 'storage' && (
            <Stack spacing={3}>
              <Box>
                <Typography variant="h6">Disks</Typography>
                <Typography variant="body2" color="text.secondary" sx={{ mb: 1.5 }}>Disk devices can only be added or removed while the VM is stopped.</Typography>
                <Stack spacing={1}>
                  {(vm.disks ?? []).map((disk) => (
                    <Stack key={disk.id} direction="row" spacing={1} sx={{ alignItems: 'center', justifyContent: 'space-between', border: 1, borderColor: 'divider', borderRadius: 1.5, p: 1.25 }}>
                      <Box>
                        <Typography variant="body2" sx={{ fontWeight: 650 }}>{disk.system ? 'System disk' : 'Data disk'}</Typography>
                        <Typography variant="caption" color="text.secondary">{disk.size_gib} GiB · qcow2</Typography>
                      </Box>
                      {!disk.system && <Button size="small" color="error" startIcon={<DeleteOutlineRounded />} disabled={active || busy} onClick={() => {
                        if (window.confirm(`Remove this ${disk.size_gib} GiB disk? Its data will be permanently deleted.`)) void runDeviceOperation(() => removeVMDisk(vm.id, disk.id))
                      }}>Remove</Button>}
                    </Stack>
                  ))}
                  <Stack direction="row" spacing={1} sx={{ alignItems: 'flex-start' }}>
                    <TextField size="small" type="number" label="New disk size (GiB)" value={newDiskGiB} onChange={(event) => setNewDiskGiB(Number(event.target.value))} slotProps={{ htmlInput: { min: 1, max: 2048 } }} />
                    <Button variant="outlined" startIcon={<AddRounded />} disabled={active || busy || newDiskGiB < 1 || newDiskGiB > 2048} onClick={() => void runDeviceOperation(() => addVMDisk(vm.id, newDiskGiB))}>Add disk</Button>
                  </Stack>
                </Stack>
              </Box>

              <Divider />
              <Box>
                <Typography variant="h6">CD-ROM devices</Typography>
                <Typography variant="body2" color="text.secondary" sx={{ mb: 1.5 }}>ISO changes and ejects apply immediately. Adding or removing a device requires the VM to be stopped.</Typography>
                <Stack spacing={1}>
                  {(vm.cdroms ?? []).map((cdrom, index) => (
                    <Stack key={cdrom.id} direction={{ xs: 'column', sm: 'row' }} spacing={1} sx={{ alignItems: { sm: 'center' }, border: 1, borderColor: 'divider', borderRadius: 1.5, p: 1.25 }}>
                      <Typography variant="body2" sx={{ fontWeight: 650, minWidth: 90 }}>CD-ROM {index + 1}</Typography>
                      <TextField select fullWidth size="small" label="ISO media" value={cdrom.iso_name ?? ''} disabled={stopping || busy} onChange={(event) => void runDeviceOperation(() => updateVMCDROM(vm.id, cdrom.id, event.target.value))}>
                        <MenuItem value="">Empty</MenuItem>
                        {images.map((image) => <MenuItem key={image.name} value={image.name}>{image.name} · {formatBytes(image.size_bytes)}</MenuItem>)}
                      </TextField>
                      <Button size="small" startIcon={<EjectRounded />} disabled={!cdrom.iso_name || stopping || busy} onClick={() => void runDeviceOperation(() => updateVMCDROM(vm.id, cdrom.id, ''))}>Eject</Button>
                      <Button size="small" color="error" disabled={active || busy} onClick={() => void runDeviceOperation(() => removeVMCDROM(vm.id, cdrom.id))}>Remove device</Button>
                    </Stack>
                  ))}
                  <Stack direction={{ xs: 'column', sm: 'row' }} spacing={1}>
                    <TextField select size="small" label="Initial ISO media" value={newCDROMISO} onChange={(event) => setNewCDROMISO(event.target.value)} sx={{ minWidth: 260 }}>
                      <MenuItem value="">Empty</MenuItem>
                      {images.map((image) => <MenuItem key={image.name} value={image.name}>{image.name}</MenuItem>)}
                    </TextField>
                    <Button variant="outlined" startIcon={<AddRounded />} disabled={active || busy} onClick={() => void runDeviceOperation(() => addVMCDROM(vm.id, newCDROMISO))}>Add CD-ROM</Button>
                  </Stack>
                </Stack>
              </Box>
            </Stack>
          )}

          {tab === 'networking' && (
            <Stack spacing={3} sx={{ maxWidth: 820 }}>
              <Box>
                <Typography variant="h6">Virtual NIC</Typography>
                <Typography variant="body2" color="text.secondary" sx={{ mb: 1.5 }}>Network adapter configuration can only be changed while the VM is stopped.</Typography>
                <Stack spacing={2}>
                  <TextField select label="Network mode" value={network.network_mode} disabled={active || busy} onChange={(event) => setNetwork({ ...network, network_mode: event.target.value as UpdateVMNetworkRequest['network_mode'] })}>
                    <MenuItem value="NETWORK_MODE_NAT">User-mode NAT</MenuItem>
                    <MenuItem value="NETWORK_MODE_BRIDGE">Host bridge</MenuItem>
                  </TextField>
                  {network.network_mode === 'NETWORK_MODE_BRIDGE' && (
                    <TextField select label="Bridge device" value={network.bridge_name} disabled={active || busy} onChange={(event) => setNetwork({ ...network, bridge_name: event.target.value })} helperText={
                      !(networking?.bridges ?? []).find((bridge) => bridge.name === network.bridge_name)?.usable_by_qemu
                        ? 'This bridge has failed diagnostics. Review Configuration → Networking before starting the VM.'
                        : undefined
                    }>
                      {(networking?.bridges ?? []).map((bridge) => <MenuItem key={bridge.name} value={bridge.name}>{bridge.name}{bridge.description ? ` - ${bridge.description}` : ''}{bridge.usable_by_qemu ? '' : ' · diagnostics required'}</MenuItem>)}
                    </TextField>
                  )}
                  <TextField label="MAC address" value={network.mac_address} disabled={active || busy} error={network.mac_address.length > 0 && !macValid} helperText={macValid ? 'The address must be unique across virtual machines.' : 'Enter a valid unicast MAC address.'} onChange={(event) => setNetwork({ ...network, mac_address: event.target.value })} />
                </Stack>
              </Box>

              <Divider />
              <Box>
                <Typography variant="h6" sx={{ mb: 1 }}>IPMI</Typography>
                <FormControlLabel control={<Switch checked={ipmi.enabled} onChange={(event) => setIPMI({ ...ipmi, enabled: event.target.checked })} />} label="Enable IPMI" />
                {ipmi.enabled && (
                  <Stack spacing={2} sx={{ mt: 1.5 }}>
                    <TextField select label="Management bridge" value={ipmi.bridge_name} onChange={(event) => {
                      const bridgeName = event.target.value
                      setIPMI({ ...ipmi, bridge_name: bridgeName, address: suggestIPMIAddress(bridgeName, networking?.bridges ?? [], virtualMachines, vm.id) })
                    }}>
                      {(networking?.bridges ?? []).map((bridge) => <MenuItem key={bridge.name} value={bridge.name}>{bridge.name}{bridge.dhcp?.enabled ? ' · managed subnet' : ''}</MenuItem>)}
                    </TextField>
                    <TextField label="Management IPv4 address" value={ipmi.address} onChange={(event) => setIPMI({ ...ipmi, address: event.target.value })} helperText="A unique address on this bridge; App Runner assigns it while IPMI is enabled." />
                    <TextField label="Username" value={ipmi.username} onChange={(event) => setIPMI({ ...ipmi, username: event.target.value })} slotProps={{ htmlInput: { maxLength: 16 } }} />
                    <TextField label="Password" type="password" value={ipmi.password} onChange={(event) => setIPMI({ ...ipmi, password: event.target.value })} helperText={vm.ipmi?.enabled ? 'Leave blank to retain the existing password.' : 'Required; IPMI passwords are limited to 20 bytes.'} slotProps={{ htmlInput: { maxLength: 20 } }} />
                    <Alert severity="warning">Expose IPMI only on a trusted management network. The listener uses IPMI 2.0 lanplus on UDP port 623.</Alert>
                  </Stack>
                )}
              </Box>
            </Stack>
          )}
        </Box>
      </Box>
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
  const used = new Set(vms.filter((candidate) => candidate.id !== excludeVMID && candidate.ipmi?.enabled).map((candidate) => candidate.ipmi?.address))
  for (let host = 2; host < 50; host += 1) {
    const candidate = `${octets[0]}.${octets[1]}.${octets[2]}.${host}`
    if (!used.has(candidate) && candidate !== bridge?.dhcp?.server_address) return candidate
  }
  return ''
}

function formatBytes(value: string): string {
  const bytes = Number(value)
  if (!Number.isFinite(bytes)) return value
  if (bytes >= 1024 ** 3) return `${(bytes / 1024 ** 3).toFixed(1)} GiB`
  if (bytes >= 1024 ** 2) return `${(bytes / 1024 ** 2).toFixed(1)} MiB`
  return `${bytes} bytes`
}
