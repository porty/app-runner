import { useEffect, useState } from 'react'
import {
  Alert,
  Box,
  Button,
  Card,
  CircularProgress,
  FormControlLabel,
  IconButton,
  MenuItem,
  Stack,
  Switch,
  Tab,
  Tabs,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material'
import { AddRounded, ArrowBackRounded, CheckRounded, DeleteOutlineRounded } from '@mui/icons-material'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'

import {
  createCloudInitTemplate,
  createVM,
  getNetworkingStatus,
  listCloudInitTemplates,
  listDiskImages,
  listISOs,
  listVMs,
  type CloudInitTemplate,
  type CreateVMRequest,
  type DiskImage,
  type ISOImage,
  type NetworkingStatus,
  type NetworkMode,
  type VirtualMachine,
  type VMFirmware,
} from './api'

type CreationTab = 'general' | 'storage' | 'networking' | 'cloud-init'

const dnsLabelPattern = /^(?=.{1,63}$)[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?$/
const macAddressPattern = /^(?:[0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}$/

const defaultCreateRequest: CreateVMRequest = {
  name: '', description: '', cpus: 2, memory_mib: 2048, firmware: 'VM_FIRMWARE_UNSPECIFIED',
  disks: [{ size_gib: 20 }], cdroms: [], nics: [{ mode: 'NETWORK_MODE_NAT' }],
  cloud_init_enabled: false, cloud_init_user_data: '',
}

export default function CreateVirtualMachinePage() {
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const [tab, setTab] = useState<CreationTab>(searchParams.get('tab') === 'storage' ? 'storage' : 'general')
  const [request, setRequest] = useState<CreateVMRequest>(defaultCreateRequest)
  const [images, setImages] = useState<ISOImage[]>([])
  const [diskImages, setDiskImages] = useState<DiskImage[]>([])
  const [networking, setNetworking] = useState<NetworkingStatus | null>(null)
  const [virtualMachines, setVirtualMachines] = useState<VirtualMachine[]>([])
  const [cloudInitTemplates, setCloudInitTemplates] = useState<CloudInitTemplate[]>([])
  const [selectedCloudInitTemplate, setSelectedCloudInitTemplate] = useState('')
  const [saveCloudInitTemplate, setSaveCloudInitTemplate] = useState(false)
  const [newCloudInitTemplateName, setNewCloudInitTemplateName] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    void (async () => {
      try {
        const [loadedISOs, loadedDiskImages, loadedNetworking, loadedTemplates, loadedVMs] = await Promise.all([
          listISOs(), listDiskImages(), getNetworkingStatus(), listCloudInitTemplates(), listVMs(),
        ])
        const bridges = loadedNetworking.bridges ?? []
        const suggestedBridge = bridges.find((bridge) => bridge.usable_by_qemu)?.name ?? bridges[0]?.name ?? ''
        const sourceRoot = searchParams.get('source_root')
        const sourcePath = searchParams.get('source_path') ?? ''
        const requestedCloudImage = sourceRoot === 'iso' && /\.(?:img|qcow2)$/i.test(sourcePath)
        const requestedInstaller = sourceRoot === 'iso' && /\.iso$/i.test(sourcePath)
        const useCloudImage = requestedCloudImage || (!requestedInstaller && loadedISOs.length === 0 && loadedDiskImages.length > 0)
        const cloudImage = requestedCloudImage ? sourcePath : loadedDiskImages[0]?.name ?? ''
        const installer = requestedInstaller ? sourcePath : loadedISOs[0]?.name ?? ''

        setImages(loadedISOs)
        setDiskImages(loadedDiskImages)
        setNetworking(loadedNetworking)
        setCloudInitTemplates(loadedTemplates)
        setVirtualMachines(loadedVMs)
        setRequest({
          ...defaultCreateRequest,
          disks: [useCloudImage && cloudImage ? { size_gib: 20, source_image: cloudImage } : { size_gib: 20 }],
          cdroms: !useCloudImage && installer ? [{ iso_name: installer }] : [],
          nics: [suggestedBridge ? { mode: 'NETWORK_MODE_BRIDGE', bridge_name: suggestedBridge } : { mode: 'NETWORK_MODE_NAT' }],
          cloud_init_enabled: useCloudImage,
        })
      } catch (requestError) {
        setError(requestError instanceof Error ? requestError.message : 'Unable to load virtual machine creation options')
      } finally {
        setLoading(false)
      }
    })()
  }, [searchParams])

  const bridges = networking?.bridges ?? []
  const trimmedName = request.name.trim()
  const nameExists = virtualMachines.some((vm) => vm.name.toLowerCase() === trimmedName.toLowerCase())
  const nameValid = dnsLabelPattern.test(trimmedName) && !nameExists
  const resourcesValid = request.cpus >= 1 && request.cpus <= 64 && request.memory_mib >= 256 && request.memory_mib <= 1048576
  const disksValid = request.disks.every((disk) => disk.size_gib >= 1 && disk.size_gib <= 2048)
  const nicsValid = request.nics.every((nic) =>
    (nic.mode === 'NETWORK_MODE_NAT' || Boolean(nic.bridge_name)) && (!nic.mac_address || macAddressPattern.test(nic.mac_address)))
  const trimmedTemplateName = newCloudInitTemplateName.trim()
  const templateNameExists = cloudInitTemplates.some((template) => template.name.toLowerCase() === trimmedTemplateName.toLowerCase())
  const templateNameValid = trimmedTemplateName.length > 0 && trimmedTemplateName.length <= 80 && !templateNameExists
  const cloudInitValid = request.cloud_init_user_data.length <= 1048576 && (!request.cloud_init_enabled || !saveCloudInitTemplate || templateNameValid)
  const valid = nameValid && resourcesValid && disksValid && nicsValid && cloudInitValid

  const updateDisk = (index: number, changes: Partial<CreateVMRequest['disks'][number]>) =>
    setRequest((current) => ({ ...current, disks: current.disks.map((disk, position) => position === index ? { ...disk, ...changes } : disk) }))
  const updateCDROM = (index: number, isoName: string) =>
    setRequest((current) => ({ ...current, cdroms: current.cdroms.map((cdrom, position) => position === index ? { iso_name: isoName } : cdrom) }))
  const updateNIC = (index: number, changes: Partial<CreateVMRequest['nics'][number]>) =>
    setRequest((current) => ({ ...current, nics: current.nics.map((nic, position) => position === index ? { ...nic, ...changes } : nic) }))
  const removeAt = (key: 'disks' | 'cdroms' | 'nics', index: number) =>
    setRequest((current) => ({ ...current, [key]: current[key].filter((_entry, position) => position !== index) }))

  const loadCloudInitTemplate = (name: string) => {
    setSelectedCloudInitTemplate(name)
    const template = cloudInitTemplates.find((candidate) => candidate.name === name)
    if (template) setRequest((current) => ({ ...current, cloud_init_enabled: true, cloud_init_user_data: template.user_data }))
  }

  const submit = async () => {
    if (!valid) return
    setBusy(true)
    setError('')
    try {
      if (request.cloud_init_enabled && saveCloudInitTemplate) {
        await createCloudInitTemplate(trimmedTemplateName, request.cloud_init_user_data)
        setSaveCloudInitTemplate(false)
      }
      await createVM({
        ...request,
        name: trimmedName,
        description: request.description?.trim(),
        nics: request.nics.map((nic) => ({ ...nic, mac_address: nic.mac_address?.trim() || undefined })),
      })
      navigate('/compute/virtual-machines')
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'Unable to create the virtual machine')
    } finally {
      setBusy(false)
    }
  }

  if (loading) return <Box sx={{ minHeight: 320, display: 'grid', placeItems: 'center' }}><CircularProgress /></Box>

  return (
    <Box>
      <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2} sx={{ justifyContent: 'space-between', alignItems: { sm: 'center' }, mb: 3 }}>
        <Box>
          <Button component={Link} to="/compute/virtual-machines" startIcon={<ArrowBackRounded />} sx={{ mb: 1 }}>Virtual machines</Button>
          <Typography variant="h4">Create virtual machine</Typography>
          <Typography color="text.secondary" sx={{ mt: 0.75 }}>Configure the workload, storage, network, and first-boot data.</Typography>
        </Box>
        <Button variant="contained" startIcon={<CheckRounded />} onClick={() => void submit()} disabled={busy || !valid} sx={{ alignSelf: { xs: 'stretch', sm: 'center' } }}>
          {busy ? 'Creating…' : 'Create virtual machine'}
        </Button>
      </Stack>

      {error && <Alert severity="error" onClose={() => setError('')} sx={{ mb: 2.5 }}>{error}</Alert>}

      <Card variant="outlined" sx={{ overflow: 'hidden' }}>
        <Tabs value={tab} onChange={(_event, value: CreationTab) => setTab(value)} variant="scrollable" scrollButtons="auto" sx={{ px: 2, borderBottom: 1, borderColor: 'divider' }}>
          <Tab value="general" label="General" />
          <Tab value="storage" label={`Storage (${request.disks.length + request.cdroms.length})`} />
          <Tab value="networking" label={`Networking (${request.nics.length})`} />
          <Tab value="cloud-init" label="Cloud Init" />
        </Tabs>

        <Box sx={{ p: { xs: 2, md: 3 }, minHeight: 420 }}>
          {tab === 'general' && (
            <Stack spacing={2.25} sx={{ maxWidth: 760 }}>
              <Box>
                <Typography variant="h6">Identity and resources</Typography>
                <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>Choose the VM hostname and initial compute allocation.</Typography>
              </Box>
              <TextField
                label="Name"
                autoFocus
                value={request.name}
                onChange={(event) => setRequest({ ...request, name: event.target.value })}
                error={trimmedName.length > 0 && !nameValid}
                helperText={nameExists ? 'A virtual machine with this name already exists.' : '1–63 letters, numbers, or interior hyphens; also used by Auto DNS.'}
              />
              <TextField label="Description" multiline minRows={2} value={request.description ?? ''} onChange={(event) => setRequest({ ...request, description: event.target.value })} slotProps={{ htmlInput: { maxLength: 4096 } }} />
              <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2}>
                <TextField fullWidth type="number" label="vCPUs" value={request.cpus} onChange={(event) => setRequest({ ...request, cpus: Number(event.target.value) })} slotProps={{ htmlInput: { min: 1, max: 64 } }} />
                <TextField fullWidth type="number" label="Memory (MiB)" value={request.memory_mib} onChange={(event) => setRequest({ ...request, memory_mib: Number(event.target.value) })} slotProps={{ htmlInput: { min: 256, max: 1048576, step: 256 } }} />
              </Stack>
              <TextField select label="Firmware" value={request.firmware} onChange={(event) => setRequest({ ...request, firmware: event.target.value as VMFirmware })} helperText="Automatic selects UEFI when a disk overlays a cloud image and BIOS otherwise.">
                <MenuItem value="VM_FIRMWARE_UNSPECIFIED">Automatic</MenuItem>
                <MenuItem value="VM_FIRMWARE_BIOS">BIOS (SeaBIOS)</MenuItem>
                <MenuItem value="VM_FIRMWARE_UEFI">UEFI (OVMF)</MenuItem>
              </TextField>
            </Stack>
          )}

          {tab === 'storage' && (
            <Stack spacing={3} sx={{ maxWidth: 880 }}>
              <Box>
                <Typography variant="h6">Disks</Typography>
                <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5, mb: 1.5 }}>The first disk is the boot disk. A disk may be a linked overlay of a qcow2 cloud image, whose backing file must remain in ISO storage.</Typography>
                <Stack spacing={1}>
                  {request.disks.map((disk, index) => (
                    <Stack key={index} direction={{ xs: 'column', sm: 'row' }} spacing={1} sx={{ alignItems: { sm: 'center' }, border: 1, borderColor: 'divider', borderRadius: 1.5, p: 1.25 }}>
                      <Typography variant="body2" sx={{ fontWeight: 650, minWidth: 90 }}>{index === 0 ? 'Boot disk' : `Disk ${index + 1}`}</Typography>
                      <TextField size="small" type="number" label="Size (GiB)" value={disk.size_gib} onChange={(event) => updateDisk(index, { size_gib: Number(event.target.value) })} error={disk.size_gib < 1 || disk.size_gib > 2048} slotProps={{ htmlInput: { min: 1, max: 2048 } }} sx={{ width: 150 }} />
                      <TextField select fullWidth size="small" label="Cloud image" value={disk.source_image ?? ''} onChange={(event) => updateDisk(index, { source_image: event.target.value || undefined })} helperText={disk.source_image ? 'The disk must be at least as large as the image’s virtual size.' : undefined}>
                        <MenuItem value="">None (empty disk)</MenuItem>
                        {diskImages.map((image) => <MenuItem key={image.name} value={image.name}>{image.name} · {formatBytes(image.size_bytes)}</MenuItem>)}
                      </TextField>
                      <Tooltip title="Remove disk"><IconButton aria-label={`Remove disk ${index + 1}`} onClick={() => removeAt('disks', index)}><DeleteOutlineRounded /></IconButton></Tooltip>
                    </Stack>
                  ))}
                  <Box><Button variant="outlined" startIcon={<AddRounded />} onClick={() => setRequest({ ...request, disks: [...request.disks, { size_gib: 20 }] })}>Add disk</Button></Box>
                </Stack>
              </Box>
              <Box>
                <Typography variant="h6">CD-ROM drives</Typography>
                <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5, mb: 1.5 }}>Attach installer media from ISO storage. Drives may also be created empty.</Typography>
                {images.length === 0 && request.cdroms.length === 0 && <Alert severity="info" sx={{ mb: 1.5 }}>No installer ISOs are in ISO storage.</Alert>}
                <Stack spacing={1}>
                  {request.cdroms.map((cdrom, index) => (
                    <Stack key={index} direction={{ xs: 'column', sm: 'row' }} spacing={1} sx={{ alignItems: { sm: 'center' }, border: 1, borderColor: 'divider', borderRadius: 1.5, p: 1.25 }}>
                      <Typography variant="body2" sx={{ fontWeight: 650, minWidth: 90 }}>CD-ROM {index + 1}</Typography>
                      <TextField select fullWidth size="small" label="ISO media" value={cdrom.iso_name ?? ''} onChange={(event) => updateCDROM(index, event.target.value)}>
                        <MenuItem value="">Empty</MenuItem>
                        {images.map((image) => <MenuItem key={image.name} value={image.name}>{image.name} · {formatBytes(image.size_bytes)}</MenuItem>)}
                      </TextField>
                      <Tooltip title="Remove drive"><IconButton aria-label={`Remove CD-ROM ${index + 1}`} onClick={() => removeAt('cdroms', index)}><DeleteOutlineRounded /></IconButton></Tooltip>
                    </Stack>
                  ))}
                  <Box><Button variant="outlined" startIcon={<AddRounded />} onClick={() => setRequest({ ...request, cdroms: [...request.cdroms, { iso_name: images[0]?.name ?? '' }] })}>Add CD-ROM</Button></Box>
                </Stack>
              </Box>
            </Stack>
          )}

          {tab === 'networking' && (
            <Stack spacing={2.25} sx={{ maxWidth: 880 }}>
              <Box>
                <Typography variant="h6">Network interfaces</Typography>
                <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>Each interface uses user-mode NAT or attaches to a host bridge. MAC addresses are generated unless specified.</Typography>
              </Box>
              {request.nics.length === 0 && <Alert severity="info">This virtual machine will have no network connectivity.</Alert>}
              {request.nics.map((nic, index) => {
                const bridge = bridges.find((candidate) => candidate.name === nic.bridge_name)
                return (
                  <Stack key={index} direction={{ xs: 'column', md: 'row' }} spacing={1} sx={{ alignItems: { md: 'flex-start' }, border: 1, borderColor: 'divider', borderRadius: 1.5, p: 1.25 }}>
                    <Typography variant="body2" sx={{ fontWeight: 650, minWidth: 70, pt: { md: 1.25 } }}>NIC {index + 1}</Typography>
                    <TextField select size="small" label="Mode" value={nic.mode} onChange={(event) => updateNIC(index, { mode: event.target.value as NetworkMode, bridge_name: event.target.value === 'NETWORK_MODE_BRIDGE' ? nic.bridge_name || bridges[0]?.name || '' : undefined })} sx={{ minWidth: 170 }}>
                      <MenuItem value="NETWORK_MODE_NAT">User-mode NAT</MenuItem>
                      <MenuItem value="NETWORK_MODE_BRIDGE">Host bridge</MenuItem>
                    </TextField>
                    {nic.mode === 'NETWORK_MODE_BRIDGE' && (
                      <TextField select size="small" fullWidth label="Bridge" value={nic.bridge_name ?? ''} onChange={(event) => updateNIC(index, { bridge_name: event.target.value })} error={!nic.bridge_name} helperText={
                        bridges.length === 0 ? 'No Linux bridges were detected. Create one in Configuration → Networking.' :
                          bridge && !bridge.usable_by_qemu ? 'This bridge has failed diagnostics. Review Configuration → Networking before starting the VM.' : undefined
                      }>
                        {bridges.map((candidate) => <MenuItem key={candidate.name} value={candidate.name}>{candidate.name}{candidate.description ? ` - ${candidate.description}` : ''}{candidate.usable_by_qemu ? '' : ' · diagnostics required'}</MenuItem>)}
                      </TextField>
                    )}
                    <TextField size="small" label="MAC address (optional)" value={nic.mac_address ?? ''} onChange={(event) => updateNIC(index, { mac_address: event.target.value })} error={Boolean(nic.mac_address) && !macAddressPattern.test(nic.mac_address ?? '')} sx={{ minWidth: 210 }} />
                    <Tooltip title="Remove interface"><IconButton aria-label={`Remove NIC ${index + 1}`} onClick={() => removeAt('nics', index)}><DeleteOutlineRounded /></IconButton></Tooltip>
                  </Stack>
                )
              })}
              <Box><Button variant="outlined" startIcon={<AddRounded />} onClick={() => setRequest({ ...request, nics: [...request.nics, { mode: 'NETWORK_MODE_NAT' }] })}>Add interface</Button></Box>
            </Stack>
          )}

          {tab === 'cloud-init' && (
            <Stack spacing={2.25} sx={{ maxWidth: 820 }}>
              <Box>
                <Typography variant="h6">NoCloud user-data</Typography>
                <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>A cidata ISO with generated per-VM meta-data is attached at first boot.</Typography>
              </Box>
              <FormControlLabel control={<Switch checked={request.cloud_init_enabled} onChange={(event) => setRequest({ ...request, cloud_init_enabled: event.target.checked })} />} label="Enable Cloud Init" />
              {request.cloud_init_enabled && (
                <>
                  <TextField select label="Load user-data template" value={selectedCloudInitTemplate} onChange={(event) => loadCloudInitTemplate(event.target.value)} helperText="Template contents are copied into this VM and can be customised here.">
                    <MenuItem value="">Custom / blank</MenuItem>
                    {cloudInitTemplates.map((template) => <MenuItem key={template.name} value={template.name}>{template.name}</MenuItem>)}
                  </TextField>
                  <TextField label="user-data" multiline minRows={12} value={request.cloud_init_user_data} onChange={(event) => setRequest({ ...request, cloud_init_user_data: event.target.value })} placeholder={'#cloud-config\nusers:\n  - name: app'} helperText="Written verbatim. Maximum size: 1 MiB." sx={{ '& .MuiInputBase-input': { fontFamily: 'monospace' } }} slotProps={{ htmlInput: { maxLength: 1048576, spellCheck: false } }} />
                  <FormControlLabel control={<Switch checked={saveCloudInitTemplate} onChange={(event) => setSaveCloudInitTemplate(event.target.checked)} />} label="Save this user-data as a new template" />
                  {saveCloudInitTemplate && (
                    <TextField label="New template name" value={newCloudInitTemplateName} onChange={(event) => setNewCloudInitTemplateName(event.target.value)} error={templateNameExists || newCloudInitTemplateName.length > 80} helperText={templateNameExists ? 'A template with this name already exists.' : 'The template remains independent of the new VM.'} slotProps={{ htmlInput: { maxLength: 80 } }} />
                  )}
                </>
              )}
            </Stack>
          )}
        </Box>
      </Card>
    </Box>
  )
}

function formatBytes(value: string): string {
  const bytes = Number(value)
  if (!Number.isFinite(bytes)) return value
  if (bytes >= 1024 ** 3) return `${(bytes / 1024 ** 3).toFixed(1)} GiB`
  if (bytes >= 1024 ** 2) return `${(bytes / 1024 ** 2).toFixed(1)} MiB`
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(1)} KiB`
  return `${bytes} bytes`
}
