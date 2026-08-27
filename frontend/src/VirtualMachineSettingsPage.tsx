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
  addVMNIC,
  configureVMCloudInit,
  configureVMIPMI,
  getNetworkingStatus,
  getVM,
  listCloudInitTemplates,
  listDiskImages,
  listISOs,
  listVMs,
  removeVMCDROM,
  removeVMDisk,
  removeVMNIC,
  updateVM,
  updateVMCDROM,
  updateVMNIC,
  type CloudInitTemplate,
  type DiskImage,
  type ISOImage,
  type NetworkingStatus,
  type NetworkMode,
  type UpdateVMRequest,
  type VirtualMachine,
  type VMCloudInitConfiguration,
  type VMIPMIConfiguration,
  type VMNIC,
  type VMNICSpec,
} from './api'

const dnsLabelPattern = /^(?=.{1,63}$)[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?$/
const macAddressPattern = /^(?:[0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}$/

type SettingsTab = 'general' | 'storage' | 'cloud-init' | 'networking'

function nicSpecValid(spec: VMNICSpec): boolean {
  if (spec.mode === 'NETWORK_MODE_BRIDGE' && !spec.bridge_name) return false
  if (spec.mac_address && !(macAddressPattern.test(spec.mac_address) && (Number.parseInt(spec.mac_address.slice(0, 2), 16) & 1) === 0)) return false
  return true
}

export default function VirtualMachineSettingsPage() {
  const { id = '' } = useParams()
  const [vm, setVM] = useState<VirtualMachine | null>(null)
  const [images, setImages] = useState<ISOImage[]>([])
  const [diskImages, setDiskImages] = useState<DiskImage[]>([])
  const [networking, setNetworking] = useState<NetworkingStatus | null>(null)
  const [virtualMachines, setVirtualMachines] = useState<VirtualMachine[]>([])
  const [general, setGeneral] = useState<UpdateVMRequest | null>(null)
  const [ipmi, setIPMI] = useState<VMIPMIConfiguration | null>(null)
  const [cloudInit, setCloudInit] = useState<VMCloudInitConfiguration | null>(null)
  const [cloudInitTemplates, setCloudInitTemplates] = useState<CloudInitTemplate[]>([])
  const [selectedCloudInitTemplate, setSelectedCloudInitTemplate] = useState('')
  const [tab, setTab] = useState<SettingsTab>('general')
  const [newDiskGiB, setNewDiskGiB] = useState(20)
  const [newDiskImage, setNewDiskImage] = useState('')
  const [newCDROMISO, setNewCDROMISO] = useState('')
  const [newNIC, setNewNIC] = useState<VMNICSpec>({ mode: 'NETWORK_MODE_NAT' })
  const [nicEdits, setNICEdits] = useState<Record<string, VMNICSpec>>({})
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const applyLoadedVM = useCallback((loadedVM: VirtualMachine, bridges: NetworkingStatus['bridges'], vms: VirtualMachine[]) => {
    const ipmiBridge = loadedVM.ipmi?.bridge_name || (bridges ?? []).find((bridge) => bridge.dhcp?.enabled)?.name || (bridges ?? [])[0]?.name || ''
    setVM(loadedVM)
    setGeneral({
      id: loadedVM.id,
      name: loadedVM.name,
      description: loadedVM.description ?? '',
      cpus: loadedVM.cpus,
      memory_mib: loadedVM.memory_mib,
    })
    setIPMI({
      id: loadedVM.id,
      enabled: loadedVM.ipmi?.enabled ?? false,
      bridge_name: ipmiBridge,
      address: loadedVM.ipmi?.address || suggestIPMIAddress(ipmiBridge, bridges, vms, loadedVM.id),
      username: loadedVM.ipmi?.username || 'admin',
      password: '',
    })
    setCloudInit({
      id: loadedVM.id,
      enabled: loadedVM.cloud_init?.enabled ?? false,
      user_data: loadedVM.cloud_init?.user_data ?? '',
    })
    setNICEdits(Object.fromEntries((loadedVM.nics ?? []).map((nic) => [nic.id, { mode: nic.mode, bridge_name: nic.bridge_name ?? '', mac_address: nic.mac_address }])))
  }, [])

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [loadedVM, loadedImages, loadedDiskImages, loadedNetworking, loadedVMs, loadedTemplates] = await Promise.all([
        getVM(id), listISOs(), listDiskImages(), getNetworkingStatus(), listVMs(), listCloudInitTemplates(),
      ])
      setImages(loadedImages)
      setDiskImages(loadedDiskImages)
      setNetworking(loadedNetworking)
      setVirtualMachines(loadedVMs)
      setCloudInitTemplates(loadedTemplates)
      setSelectedCloudInitTemplate('')
      setNewCDROMISO(loadedImages[0]?.name ?? '')
      const bridges = loadedNetworking.bridges ?? []
      setNewNIC(bridges[0] ? { mode: 'NETWORK_MODE_BRIDGE', bridge_name: bridges[0].name } : { mode: 'NETWORK_MODE_NAT' })
      applyLoadedVM(loadedVM, bridges, loadedVMs)
      setError('')
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'Unable to load virtual machine settings')
    } finally {
      setLoading(false)
    }
  }, [applyLoadedVM, id])

  useEffect(() => { void load() }, [load])

  const runDeviceOperation = async (operation: () => Promise<VirtualMachine>) => {
    setBusy(true)
    setError('')
    try {
      const updated = await operation()
      setVM(updated)
      setNICEdits(Object.fromEntries((updated.nics ?? []).map((nic) => [nic.id, { mode: nic.mode, bridge_name: nic.bridge_name ?? '', mac_address: nic.mac_address }])))
      setVirtualMachines((current) => current.map((candidate) => candidate.id === updated.id ? updated : candidate))
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'Unable to update virtual machine devices')
    } finally {
      setBusy(false)
    }
  }

  if (loading) return <Box sx={{ display: 'grid', placeItems: 'center', minHeight: 320 }}><CircularProgress /></Box>
  if (!vm || !general || !ipmi || !cloudInit) return <Alert severity="error">{error || 'Virtual machine settings are unavailable.'}</Alert>

  const running = vm.status === 'VM_STATUS_RUNNING'
  const stopping = vm.status === 'VM_STATUS_STOPPING'
  const active = running || stopping
  const bridges = networking?.bridges ?? []
  const nameValid = dnsLabelPattern.test(general.name.trim())
  const generalDirty = general.name.trim() !== vm.name || general.description.trim() !== (vm.description ?? '') || general.cpus !== vm.cpus || general.memory_mib !== vm.memory_mib
  const ipmiDirty = ipmi.enabled !== (vm.ipmi?.enabled ?? false) || (ipmi.enabled && (
    ipmi.bridge_name !== (vm.ipmi?.bridge_name ?? '') || ipmi.address !== (vm.ipmi?.address ?? '') || ipmi.username !== (vm.ipmi?.username ?? '') || ipmi.password !== ''))
  const ipmiValid = !ipmi.enabled || Boolean(ipmi.bridge_name && ipmi.address && ipmi.username && (vm.ipmi?.enabled || ipmi.password))
  const cloudInitDirty = cloudInit.enabled !== (vm.cloud_init?.enabled ?? false) || cloudInit.user_data !== (vm.cloud_init?.user_data ?? '')
  const saveDisabled = busy || !nameValid || general.cpus < 1 || general.cpus > 64 || general.memory_mib < 256 || general.memory_mib > 1048576 ||
    !ipmiValid || (active && cloudInitDirty) || (!generalDirty && !ipmiDirty && !cloudInitDirty)

  // Each section is only sent when it changed, so an unchanged IPMI
  // configuration never restarts the virtual BMC.
  const save = async () => {
    setBusy(true)
    setError('')
    try {
      if (generalDirty) await updateVM({ ...general, name: general.name.trim(), description: general.description.trim() })
      if (ipmiDirty) await configureVMIPMI(ipmi)
      if (cloudInitDirty) await configureVMCloudInit(cloudInit)
      applyLoadedVM(await getVM(id), bridges, virtualMachines)
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'Unable to save virtual machine settings')
    } finally {
      setBusy(false)
    }
  }

  const nicChanged = (nic: VMNIC, edit: VMNICSpec | undefined) =>
    Boolean(edit) && (edit!.mode !== nic.mode || (edit!.bridge_name ?? '') !== (nic.bridge_name ?? '') || (edit!.mac_address ?? '').toLowerCase() !== nic.mac_address.toLowerCase())

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
          <Tab value="cloud-init" label="Cloud Init" />
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
              <Typography variant="body2" color="text.secondary">Firmware: {vm.firmware === 'VM_FIRMWARE_UEFI' ? 'UEFI (OVMF)' : 'BIOS (SeaBIOS)'}</Typography>
            </Stack>
          )}

          {tab === 'storage' && (
            <Stack spacing={3}>
              <Box>
                <Typography variant="h6">Disks</Typography>
                <Typography variant="body2" color="text.secondary" sx={{ mb: 1.5 }}>The first disk is the boot disk. Disks can only be added or removed while the VM is stopped.</Typography>
                <Stack spacing={1}>
                  {(vm.disks ?? []).length === 0 && <Typography variant="body2" color="text.secondary">This virtual machine has no disks.</Typography>}
                  {(vm.disks ?? []).map((disk, index) => (
                    <Stack key={disk.id} direction="row" spacing={1} sx={{ alignItems: 'center', justifyContent: 'space-between', border: 1, borderColor: 'divider', borderRadius: 1.5, p: 1.25 }}>
                      <Box>
                        <Typography variant="body2" sx={{ fontWeight: 650 }}>{index === 0 ? 'Boot disk' : `Disk ${index + 1}`}</Typography>
                        <Typography variant="caption" color="text.secondary">
                          {disk.size_gib} GiB · qcow2{disk.source_image ? ` · linked to iso/${disk.source_image}` : ''}
                        </Typography>
                      </Box>
                      <Button size="small" color="error" startIcon={<DeleteOutlineRounded />} disabled={active || busy} onClick={() => {
                        if (window.confirm(`Remove this ${disk.size_gib} GiB disk? Its data will be permanently deleted.`)) void runDeviceOperation(() => removeVMDisk(vm.id, disk.id))
                      }}>Remove</Button>
                    </Stack>
                  ))}
                  <Stack direction={{ xs: 'column', sm: 'row' }} spacing={1} sx={{ alignItems: { sm: 'flex-start' } }}>
                    <TextField size="small" type="number" label="New disk size (GiB)" value={newDiskGiB} onChange={(event) => setNewDiskGiB(Number(event.target.value))} slotProps={{ htmlInput: { min: 1, max: 2048 } }} />
                    <TextField select size="small" label="Cloud image" value={newDiskImage} onChange={(event) => setNewDiskImage(event.target.value)} sx={{ minWidth: 260 }}>
                      <MenuItem value="">None (empty disk)</MenuItem>
                      {diskImages.map((image) => <MenuItem key={image.name} value={image.name}>{image.name}</MenuItem>)}
                    </TextField>
                    <Button variant="outlined" startIcon={<AddRounded />} disabled={active || busy || newDiskGiB < 1 || newDiskGiB > 2048} onClick={() => void runDeviceOperation(() => addVMDisk(vm.id, { size_gib: newDiskGiB, source_image: newDiskImage || undefined }))}>Add disk</Button>
                  </Stack>
                </Stack>
              </Box>

              <Divider />
              <Box>
                <Typography variant="h6">CD-ROM devices</Typography>
                <Typography variant="body2" color="text.secondary" sx={{ mb: 1.5 }}>ISO changes and ejects apply immediately. Adding or removing a device requires the VM to be stopped.</Typography>
                <Stack spacing={1}>
                  {(vm.cdroms ?? []).length === 0 && <Typography variant="body2" color="text.secondary">This virtual machine has no CD-ROM drives.</Typography>}
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

          {tab === 'cloud-init' && (
            <Stack spacing={2.25} sx={{ maxWidth: 820 }}>
              <Box>
                <Typography variant="h6">NoCloud configuration</Typography>
                <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
                  App Runner creates a cidata ISO containing user-data and generated per-VM meta-data. Changes require the VM to be stopped.
                </Typography>
              </Box>
              {active && <Alert severity="info">Stop the virtual machine before changing its Cloud Init configuration.</Alert>}
              <FormControlLabel
                control={<Switch checked={cloudInit.enabled} disabled={active || busy} onChange={(event) => setCloudInit({ ...cloudInit, enabled: event.target.checked })} />}
                label="Enable Cloud Init"
              />
              <TextField
                select
                label="Load user-data template"
                value={selectedCloudInitTemplate}
                disabled={active || busy}
                onChange={(event) => {
                  const name = event.target.value
                  setSelectedCloudInitTemplate(name)
                  const template = cloudInitTemplates.find((candidate) => candidate.name === name)
                  if (template) setCloudInit({ ...cloudInit, enabled: true, user_data: template.user_data })
                }}
                helperText="Loading a template copies its contents into this VM."
              >
                <MenuItem value="">Choose a template</MenuItem>
                {cloudInitTemplates.map((template) => <MenuItem key={template.name} value={template.name}>{template.name}</MenuItem>)}
              </TextField>
              <TextField
                label="user-data"
                multiline
                minRows={14}
                value={cloudInit.user_data}
                disabled={!cloudInit.enabled || active || busy}
                onChange={(event) => setCloudInit({ ...cloudInit, user_data: event.target.value })}
                placeholder={'#cloud-config\nusers:\n  - name: app'}
                helperText="The content is written verbatim; cloud-config YAML and executable user-data scripts are both supported. Maximum size: 1 MiB."
                slotProps={{ htmlInput: { maxLength: 1048576, spellCheck: false } }}
              />
            </Stack>
          )}

          {tab === 'networking' && (
            <Stack spacing={3} sx={{ maxWidth: 880 }}>
              <Box>
                <Typography variant="h6">Network interfaces</Typography>
                <Typography variant="body2" color="text.secondary" sx={{ mb: 1.5 }}>Interfaces can only be added, changed, or removed while the VM is stopped.</Typography>
                <Stack spacing={1}>
                  {(vm.nics ?? []).length === 0 && <Typography variant="body2" color="text.secondary">This virtual machine has no network interfaces.</Typography>}
                  {(vm.nics ?? []).map((nic, index) => {
                    const edit = nicEdits[nic.id] ?? { mode: nic.mode, bridge_name: nic.bridge_name ?? '', mac_address: nic.mac_address }
                    const bridge = bridges.find((candidate) => candidate.name === edit.bridge_name)
                    const setEdit = (changes: Partial<VMNICSpec>) => setNICEdits((current) => ({ ...current, [nic.id]: { ...edit, ...changes } }))
                    return (
                      <Stack key={nic.id} direction={{ xs: 'column', md: 'row' }} spacing={1} sx={{ alignItems: { md: 'flex-start' }, border: 1, borderColor: 'divider', borderRadius: 1.5, p: 1.25 }}>
                        <Typography variant="body2" sx={{ fontWeight: 650, minWidth: 70, pt: { md: 1.25 } }}>NIC {index + 1}</Typography>
                        <TextField select size="small" label="Mode" value={edit.mode} disabled={active || busy} onChange={(event) => setEdit({ mode: event.target.value as NetworkMode, bridge_name: event.target.value === 'NETWORK_MODE_BRIDGE' ? edit.bridge_name || bridges[0]?.name || '' : '' })} sx={{ minWidth: 170 }}>
                          <MenuItem value="NETWORK_MODE_NAT">User-mode NAT</MenuItem>
                          <MenuItem value="NETWORK_MODE_BRIDGE">Host bridge</MenuItem>
                        </TextField>
                        {edit.mode === 'NETWORK_MODE_BRIDGE' && (
                          <TextField select size="small" fullWidth label="Bridge" value={edit.bridge_name ?? ''} disabled={active || busy} onChange={(event) => setEdit({ bridge_name: event.target.value })} error={!edit.bridge_name} helperText={bridge && !bridge.usable_by_qemu ? 'This bridge has failed diagnostics. Review Configuration → Networking before starting the VM.' : undefined}>
                            {bridges.map((candidate) => <MenuItem key={candidate.name} value={candidate.name}>{candidate.name}{candidate.description ? ` - ${candidate.description}` : ''}{candidate.usable_by_qemu ? '' : ' · diagnostics required'}</MenuItem>)}
                          </TextField>
                        )}
                        <TextField size="small" label="MAC address" value={edit.mac_address ?? ''} disabled={active || busy} error={!nicSpecValid(edit)} onChange={(event) => setEdit({ mac_address: event.target.value })} sx={{ minWidth: 210 }} />
                        <Button size="small" variant="outlined" disabled={active || busy || !nicChanged(nic, edit) || !nicSpecValid(edit)} onClick={() => void runDeviceOperation(() => updateVMNIC(vm.id, nic.id, edit))}>Apply</Button>
                        <Button size="small" color="error" disabled={active || busy} onClick={() => void runDeviceOperation(() => removeVMNIC(vm.id, nic.id))}>Remove</Button>
                      </Stack>
                    )
                  })}
                  <Stack direction={{ xs: 'column', md: 'row' }} spacing={1} sx={{ alignItems: { md: 'flex-start' } }}>
                    <TextField select size="small" label="New interface mode" value={newNIC.mode} onChange={(event) => setNewNIC({ ...newNIC, mode: event.target.value as NetworkMode, bridge_name: event.target.value === 'NETWORK_MODE_BRIDGE' ? newNIC.bridge_name || bridges[0]?.name || '' : '' })} sx={{ minWidth: 190 }}>
                      <MenuItem value="NETWORK_MODE_NAT">User-mode NAT</MenuItem>
                      <MenuItem value="NETWORK_MODE_BRIDGE">Host bridge</MenuItem>
                    </TextField>
                    {newNIC.mode === 'NETWORK_MODE_BRIDGE' && (
                      <TextField select size="small" label="Bridge" value={newNIC.bridge_name ?? ''} onChange={(event) => setNewNIC({ ...newNIC, bridge_name: event.target.value })} sx={{ minWidth: 220 }}>
                        {bridges.map((candidate) => <MenuItem key={candidate.name} value={candidate.name}>{candidate.name}</MenuItem>)}
                      </TextField>
                    )}
                    <TextField size="small" label="MAC address (optional)" value={newNIC.mac_address ?? ''} onChange={(event) => setNewNIC({ ...newNIC, mac_address: event.target.value })} error={!nicSpecValid(newNIC)} sx={{ minWidth: 210 }} />
                    <Button variant="outlined" startIcon={<AddRounded />} disabled={active || busy || !nicSpecValid(newNIC)} onClick={() => void runDeviceOperation(() => addVMNIC(vm.id, { ...newNIC, mac_address: newNIC.mac_address?.trim() || undefined }))}>Add interface</Button>
                  </Stack>
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
                      setIPMI({ ...ipmi, bridge_name: bridgeName, address: suggestIPMIAddress(bridgeName, bridges, virtualMachines, vm.id) })
                    }}>
                      {bridges.map((bridge) => <MenuItem key={bridge.name} value={bridge.name}>{bridge.name}{bridge.dhcp?.enabled ? ' · managed subnet' : ''}</MenuItem>)}
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
