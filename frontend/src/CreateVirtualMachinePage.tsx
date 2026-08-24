import { useEffect, useState } from 'react'
import {
  Alert,
  Box,
  Button,
  Card,
  CircularProgress,
  FormControlLabel,
  MenuItem,
  Stack,
  Switch,
  Tab,
  Tabs,
  TextField,
  Typography,
} from '@mui/material'
import { ArrowBackRounded, CheckRounded } from '@mui/icons-material'
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
  type VirtualMachine,
} from './api'

type CreationTab = 'general' | 'storage' | 'networking' | 'cloud-init'
type CreationSource = 'installer' | 'cloud-image'

const defaultCreateRequest: CreateVMRequest = {
  name: '', cpus: 2, memory_mib: 2048, disk_gib: 20, iso_name: '', source_image: '',
  network_mode: 'NETWORK_MODE_NAT', bridge_name: '',
  cloud_init_enabled: false, cloud_init_user_data: '',
}
const dnsLabelPattern = /^(?=.{1,63}$)[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?$/

export default function CreateVirtualMachinePage() {
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const [tab, setTab] = useState<CreationTab>(searchParams.get('tab') === 'storage' ? 'storage' : 'general')
  const [request, setRequest] = useState<CreateVMRequest>(defaultCreateRequest)
  const [creationSource, setCreationSource] = useState<CreationSource>('installer')
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
        const source: CreationSource = requestedCloudImage || (!requestedInstaller && loadedISOs.length === 0 && loadedDiskImages.length > 0)
          ? 'cloud-image'
          : 'installer'

        setImages(loadedISOs)
        setDiskImages(loadedDiskImages)
        setNetworking(loadedNetworking)
        setCloudInitTemplates(loadedTemplates)
        setVirtualMachines(loadedVMs)
        setCreationSource(source)
        setRequest({
          ...defaultCreateRequest,
          iso_name: source === 'installer' ? (requestedInstaller ? sourcePath : loadedISOs[0]?.name ?? '') : '',
          source_image: source === 'cloud-image' ? (requestedCloudImage ? sourcePath : loadedDiskImages[0]?.name ?? '') : '',
          bridge_name: suggestedBridge,
          cloud_init_enabled: source === 'cloud-image',
        })
      } catch (requestError) {
        setError(requestError instanceof Error ? requestError.message : 'Unable to load virtual machine creation options')
      } finally {
        setLoading(false)
      }
    })()
  }, [searchParams])

  const trimmedName = request.name.trim()
  const nameExists = virtualMachines.some((vm) => vm.name.toLowerCase() === trimmedName.toLowerCase())
  const nameValid = dnsLabelPattern.test(trimmedName) && !nameExists
  const resourcesValid = request.cpus >= 1 && request.cpus <= 64 && request.memory_mib >= 256 && request.memory_mib <= 1048576
  const storageValid = request.disk_gib >= 1 && request.disk_gib <= 2048 && (creationSource === 'installer' ? Boolean(request.iso_name) : Boolean(request.source_image))
  const networkingValid = request.network_mode === 'NETWORK_MODE_NAT' || Boolean(request.bridge_name)
  const trimmedTemplateName = newCloudInitTemplateName.trim()
  const templateNameExists = cloudInitTemplates.some((template) => template.name.toLowerCase() === trimmedTemplateName.toLowerCase())
  const templateNameValid = trimmedTemplateName.length > 0 && trimmedTemplateName.length <= 80 && !templateNameExists
  const cloudInitValid = request.cloud_init_user_data.length <= 1048576 && (!request.cloud_init_enabled || !saveCloudInitTemplate || templateNameValid)
  const valid = nameValid && resourcesValid && storageValid && networkingValid && cloudInitValid

  const selectCreationSource = (source: CreationSource) => {
    setCreationSource(source)
    setRequest((current) => ({
      ...current,
      iso_name: source === 'installer' ? (current.iso_name || images[0]?.name || '') : '',
      source_image: source === 'cloud-image' ? (current.source_image || diskImages[0]?.name || '') : '',
      cloud_init_enabled: source === 'cloud-image' ? true : current.cloud_init_enabled,
    }))
  }

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
        iso_name: creationSource === 'installer' ? request.iso_name : '',
        source_image: creationSource === 'cloud-image' ? request.source_image : '',
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
          <Tab value="storage" label="Storage" />
          <Tab value="networking" label="Networking" />
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
              <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2}>
                <TextField fullWidth type="number" label="vCPUs" value={request.cpus} onChange={(event) => setRequest({ ...request, cpus: Number(event.target.value) })} slotProps={{ htmlInput: { min: 1, max: 64 } }} />
                <TextField fullWidth type="number" label="Memory (MiB)" value={request.memory_mib} onChange={(event) => setRequest({ ...request, memory_mib: Number(event.target.value) })} slotProps={{ htmlInput: { min: 256, max: 1048576, step: 256 } }} />
              </Stack>
            </Stack>
          )}

          {tab === 'storage' && (
            <Stack spacing={2.25} sx={{ maxWidth: 820 }}>
              <Box>
                <Typography variant="h6">Boot source and system disk</Typography>
                <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>Install to a new disk or create a linked overlay from a cloud image.</Typography>
              </Box>
              {images.length === 0 && diskImages.length === 0 && <Alert severity="warning">Add an installer ISO or qcow2 cloud image to ISO storage before creating a VM.</Alert>}
              <TextField select label="Creation source" value={creationSource} onChange={(event) => selectCreationSource(event.target.value as CreationSource)}>
                <MenuItem value="installer" disabled={images.length === 0}>Installation ISO</MenuItem>
                <MenuItem value="cloud-image" disabled={diskImages.length === 0}>Cloud disk image (linked clone)</MenuItem>
              </TextField>
              {creationSource === 'installer' ? (
                <TextField select label="Installation ISO" value={request.iso_name} onChange={(event) => setRequest({ ...request, iso_name: event.target.value })}>
                  {images.map((image) => <MenuItem key={image.name} value={image.name}>{image.name} · {formatBytes(image.size_bytes)}</MenuItem>)}
                </TextField>
              ) : (
                <TextField select label="Cloud disk image" value={request.source_image} onChange={(event) => setRequest({ ...request, source_image: event.target.value })} helperText="The qcow2 overlay boots with UEFI. Its backing image must remain in ISO storage.">
                  {diskImages.map((image) => <MenuItem key={image.name} value={image.name}>{image.name} · {formatBytes(image.size_bytes)}</MenuItem>)}
                </TextField>
              )}
              <TextField type="number" label="System disk (GiB)" value={request.disk_gib} onChange={(event) => setRequest({ ...request, disk_gib: Number(event.target.value) })} helperText={creationSource === 'cloud-image' ? 'Must be at least as large as the cloud image’s virtual size.' : undefined} slotProps={{ htmlInput: { min: 1, max: 2048 } }} />
            </Stack>
          )}

          {tab === 'networking' && (
            <Stack spacing={2.25} sx={{ maxWidth: 760 }}>
              <Box>
                <Typography variant="h6">Primary network adapter</Typography>
                <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>The MAC address is generated automatically when the VM is created.</Typography>
              </Box>
              <TextField select label="Network mode" value={request.network_mode} onChange={(event) => setRequest({ ...request, network_mode: event.target.value as CreateVMRequest['network_mode'] })}>
                <MenuItem value="NETWORK_MODE_NAT">User-mode NAT</MenuItem>
                <MenuItem value="NETWORK_MODE_BRIDGE">Host bridge</MenuItem>
              </TextField>
              {request.network_mode === 'NETWORK_MODE_BRIDGE' && (
                <TextField select label="Bridge" value={request.bridge_name ?? ''} onChange={(event) => setRequest({ ...request, bridge_name: event.target.value })} helperText={
                  (networking?.bridges ?? []).length === 0 ? 'No Linux bridges were detected. Create one in Configuration → Networking.' :
                    !(networking?.bridges ?? []).find((bridge) => bridge.name === request.bridge_name)?.usable_by_qemu ? 'This bridge has failed diagnostics. Review Configuration → Networking before starting the VM.' : undefined
                }>
                  {(networking?.bridges ?? []).map((bridge) => <MenuItem key={bridge.name} value={bridge.name}>{bridge.name}{bridge.description ? ` - ${bridge.description}` : ''}{bridge.usable_by_qemu ? '' : ' · diagnostics required'}</MenuItem>)}
                </TextField>
              )}
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
