import { useCallback, useEffect, useMemo, useState, type ChangeEvent } from 'react'
import {
  Alert,
  Box,
  Button,
  Card,
  CircularProgress,
  Collapse,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Divider,
  List,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  MenuItem,
  Stack,
  Tab,
  Tabs,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material'
import {
  AlbumRounded,
  AddRounded,
  ChevronRightRounded,
  ExpandMoreRounded,
  FolderOpenRounded,
  FolderRounded,
  RefreshRounded,
  SdCardRounded,
  UploadFileRounded,
} from '@mui/icons-material'
import { Link } from 'react-router-dom'

import {
  downloadStorageFile,
  listStorage,
  uploadStorageFiles,
  type StorageNode,
  type StorageRoot,
  type StorageRootName,
} from './api'

type UploadMethod = 'device' | 'url'

function storageIcon(root: StorageRootName) {
  return root === 'iso' ? <AlbumRounded color="primary" /> : <SdCardRounded color="secondary" />
}

function storageFileIcon(root: StorageRootName, name: string) {
  if (root === 'iso' && /\.(?:img|qcow2)$/i.test(name)) return <SdCardRounded color="secondary" />
  return storageIcon(root)
}

function formatSize(bytes = 0): string {
  if (bytes < 1024) return `${bytes} B`
  const units = ['KiB', 'MiB', 'GiB', 'TiB']
  let value = bytes
  let unit = -1
  do {
    value /= 1024
    unit += 1
  } while (value >= 1024 && unit < units.length - 1)
  return `${value.toFixed(value >= 10 ? 1 : 2)} ${units[unit]}`
}

function formatModified(value?: string): string {
  if (!value) return ''
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '' : date.toLocaleString()
}

function countEntries(entries: StorageNode[]): number {
  return entries.reduce((total, entry) => total + 1 + countEntries(entry.children ?? []), 0)
}

function collectDirectories(entries: StorageNode[], result: StorageNode[] = []): StorageNode[] {
  entries.forEach((entry) => {
    if (entry.kind === 'directory') {
      result.push(entry)
      collectDirectories(entry.children ?? [], result)
    }
  })
  return result
}

function StorageEntry({ entry, root, depth }: { entry: StorageNode; root: StorageRootName; depth: number }) {
  const directory = entry.kind === 'directory'
  const [open, setOpen] = useState(false)
  const children = entry.children ?? []
  const canCreateVM = root === 'iso' && !directory && /\.(?:iso|img|qcow2)$/i.test(entry.name)
  const createVMSearch = new URLSearchParams({ tab: 'storage', source_root: root, source_path: entry.path }).toString()

  return (
    <>
      <ListItemButton
        component="div"
        onClick={directory ? () => setOpen((value) => !value) : undefined}
        aria-expanded={directory ? open : undefined}
        sx={{ minHeight: 44, pl: 1.5 + depth * 2, pr: 2, cursor: directory ? 'pointer' : 'default' }}
      >
        <Box sx={{ width: 28, display: 'grid', placeItems: 'center', color: 'text.secondary', flexShrink: 0 }}>
          {directory && (open ? <ExpandMoreRounded fontSize="small" /> : <ChevronRightRounded fontSize="small" />)}
        </Box>
        <ListItemIcon sx={{ minWidth: 38 }}>
          {directory ? (open ? <FolderOpenRounded color="warning" /> : <FolderRounded color="warning" />) : storageFileIcon(root, entry.name)}
        </ListItemIcon>
        <ListItemText
          primary={entry.name}
          secondary={directory ? `${children.length} ${children.length === 1 ? 'item' : 'items'}` : entry.path}
          slotProps={{
            primary: { noWrap: true, sx: { fontWeight: directory ? 650 : 500 } },
            secondary: { noWrap: true },
          }}
        />
        {!directory && (
          <Stack direction="row" spacing={3} sx={{ ml: 2, alignItems: 'center', color: 'text.secondary', flexShrink: 0 }}>
            <Typography variant="body2" sx={{ fontVariantNumeric: 'tabular-nums' }}>{formatSize(entry.size_bytes)}</Typography>
            <Typography variant="body2" sx={{ display: { xs: 'none', lg: 'block' }, minWidth: 168 }}>
              {formatModified(entry.modified_at)}
            </Typography>
          </Stack>
        )}
        {canCreateVM && (
          <Button
            component={Link}
            to={`/compute/virtual-machines/new?${createVMSearch}`}
            size="small"
            startIcon={<AddRounded />}
            sx={{ ml: 2, flexShrink: 0 }}
          >
            Create VM
          </Button>
        )}
      </ListItemButton>
      {directory && (
        <Collapse in={open} timeout="auto" unmountOnExit>
          {children.length > 0 ? children.map((child) => (
            <StorageEntry key={child.path} entry={child} root={root} depth={depth + 1} />
          )) : (
            <Typography variant="body2" color="text.secondary" sx={{ py: 1.5, pl: 9 + depth * 2 }}>
              Empty directory
            </Typography>
          )}
        </Collapse>
      )}
    </>
  )
}

function StorageTreeRoot({ root }: { root: StorageRoot }) {
  const [open, setOpen] = useState(true)
  const count = countEntries(root.entries)

  return (
    <>
      <ListItemButton
        component="div"
        onClick={() => setOpen((value) => !value)}
        aria-expanded={open}
        sx={{ minHeight: 52, px: 2, bgcolor: 'action.hover' }}
      >
        <Box sx={{ width: 28, display: 'grid', placeItems: 'center', color: 'text.secondary', flexShrink: 0 }}>
          {open ? <ExpandMoreRounded /> : <ChevronRightRounded />}
        </Box>
        <ListItemIcon sx={{ minWidth: 40 }}>{storageIcon(root.name)}</ListItemIcon>
        <ListItemText
          primary={`${root.name}/`}
          secondary={`${count} ${count === 1 ? 'entry' : 'entries'}`}
          slotProps={{ primary: { sx: { fontWeight: 750 } } }}
        />
      </ListItemButton>
      <Collapse in={open} timeout="auto" unmountOnExit>
        {root.entries.length > 0 ? root.entries.map((entry) => (
          <StorageEntry key={entry.path} entry={entry} root={root.name} depth={1} />
        )) : (
          <Typography color="text.secondary" sx={{ py: 3, pl: 9 }}>
            This storage root is empty.
          </Typography>
        )}
      </Collapse>
    </>
  )
}

export default function StoragePage() {
  const [roots, setRoots] = useState<StorageRoot[]>([])
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [dialogOpen, setDialogOpen] = useState(false)
  const [method, setMethod] = useState<UploadMethod>('device')
  const [targetRoot, setTargetRoot] = useState<StorageRootName>('iso')
  const [targetDirectory, setTargetDirectory] = useState('')
  const [files, setFiles] = useState<File[]>([])
  const [sourceURL, setSourceURL] = useState('')
  const [filename, setFilename] = useState('')
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    try {
      setRoots(await listStorage())
      setError('')
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'Storage could not be loaded')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const directories = useMemo(() => {
    const root = roots.find((candidate) => candidate.name === targetRoot)
    return root ? collectDirectories(root.entries) : []
  }, [roots, targetRoot])

  const openUpload = () => {
    setMethod('device')
    setTargetRoot('iso')
    setTargetDirectory('')
    setFiles([])
    setSourceURL('')
    setFilename('')
    setError('')
    setDialogOpen(true)
  }

  const selectFiles = (event: ChangeEvent<HTMLInputElement>) => {
    setFiles(Array.from(event.target.files ?? []))
    event.target.value = ''
  }

  const submit = async () => {
    setBusy(true)
    setError('')
    try {
      if (method === 'device') {
        const uploaded = await uploadStorageFiles(targetRoot, targetDirectory, files)
        setSuccess(`${uploaded.length} ${uploaded.length === 1 ? 'file' : 'files'} uploaded to ${targetRoot}/${targetDirectory}`)
      } else {
        const downloaded = await downloadStorageFile({
          root: targetRoot,
          directory: targetDirectory,
          url: sourceURL.trim(),
          filename: filename.trim() || undefined,
        })
        setSuccess(`${downloaded.name} downloaded to ${targetRoot}/${targetDirectory}`)
      }
      setDialogOpen(false)
      await load()
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'The file could not be added')
    } finally {
      setBusy(false)
    }
  }

  const valid = method === 'device' ? files.length > 0 : /^https?:\/\//i.test(sourceURL.trim())

  return (
    <Box>
      <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2} sx={{ justifyContent: 'space-between', alignItems: { sm: 'flex-start' }, mb: 3.5 }}>
        <Box>
          <Typography variant="h4">Storage</Typography>
          <Typography color="text.secondary" sx={{ mt: 0.75 }}>
            Browse ISO images and virtual machine disk storage.
          </Typography>
        </Box>
        <Stack direction="row" spacing={1}>
          <Tooltip title="Refresh storage listing">
            <span><Button variant="outlined" onClick={() => void load()} disabled={loading} startIcon={<RefreshRounded />}>Refresh</Button></span>
          </Tooltip>
          <Button variant="contained" onClick={openUpload} startIcon={<UploadFileRounded />}>Add files</Button>
        </Stack>
      </Stack>

      {error && !dialogOpen && <Alert severity="error" onClose={() => setError('')} sx={{ mb: 2.5 }}>{error}</Alert>}
      {success && <Alert severity="success" onClose={() => setSuccess('')} sx={{ mb: 2.5 }}>{success}</Alert>}

      <Card variant="outlined" sx={{ overflow: 'hidden' }}>
        <Box sx={{ px: 2.5, py: 2 }}>
          <Typography variant="h6">Files</Typography>
          <Typography variant="body2" color="text.secondary">The configured storage roots and their subdirectories.</Typography>
        </Box>
        <Divider />
        {loading && roots.length === 0 ? (
          <Box sx={{ minHeight: 240, display: 'grid', placeItems: 'center' }}><CircularProgress /></Box>
        ) : (
          <List disablePadding aria-label="Storage file tree">
            {roots.map((root, index) => (
              <Box key={root.name}>
                {index > 0 && <Divider />}
                <StorageTreeRoot root={root} />
              </Box>
            ))}
          </List>
        )}
      </Card>

      <Dialog open={dialogOpen} onClose={busy ? undefined : () => setDialogOpen(false)} fullWidth maxWidth="sm">
        <DialogTitle>Add files</DialogTitle>
        <DialogContent sx={{ px: 0 }}>
          <Tabs
            value={method}
            onChange={(_event, value: UploadMethod) => { setMethod(value); setError('') }}
            variant="fullWidth"
            sx={{ borderBottom: 1, borderColor: 'divider', mb: 3 }}
          >
            <Tab value="device" label="From this device" />
            <Tab value="url" label="From a URL" />
          </Tabs>
          <Stack spacing={2.5} sx={{ px: 3 }}>
            {error && <Alert severity="error" onClose={() => setError('')}>{error}</Alert>}
            <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2}>
              <TextField
                select
                fullWidth
                label="Storage root"
                value={targetRoot}
                onChange={(event) => {
                  setTargetRoot(event.target.value as StorageRootName)
                  setTargetDirectory('')
                }}
              >
                <MenuItem value="iso">iso/ — ISO images</MenuItem>
                <MenuItem value="disk">disk/ — Disk images</MenuItem>
              </TextField>
              <TextField
                select
                fullWidth
                label="Destination"
                value={targetDirectory}
                onChange={(event) => setTargetDirectory(event.target.value)}
              >
                <MenuItem value="">{targetRoot}/</MenuItem>
                {directories.map((directory) => (
                  <MenuItem key={directory.path} value={directory.path}>{targetRoot}/{directory.path}/</MenuItem>
                ))}
              </TextField>
            </Stack>

            {method === 'device' ? (
              <Box>
                <Button component="label" variant="outlined" startIcon={<UploadFileRounded />}>
                  Choose files
                  <Box component="input" type="file" multiple hidden onChange={selectFiles} />
                </Button>
                <Typography variant="body2" color="text.secondary" sx={{ mt: 1.5 }}>
                  {files.length === 0
                    ? 'Select one or more files to upload from your browser.'
                    : files.map((file) => file.name).join(', ')}
                </Typography>
              </Box>
            ) : (
              <>
                <TextField
                  label="File URL"
                  type="url"
                  value={sourceURL}
                  onChange={(event) => setSourceURL(event.target.value)}
                  placeholder="https://example.com/image.iso"
                  helperText="The App Runner host downloads the file directly."
                  fullWidth
                  autoFocus
                />
                <TextField
                  label="Filename (optional)"
                  value={filename}
                  onChange={(event) => setFilename(event.target.value)}
                  placeholder="Inferred from the URL"
                  fullWidth
                />
              </>
            )}
          </Stack>
        </DialogContent>
        <DialogActions sx={{ px: 3, py: 2.5 }}>
          <Button onClick={() => setDialogOpen(false)} disabled={busy}>Cancel</Button>
          <Button variant="contained" onClick={() => void submit()} disabled={busy || !valid}>
            {busy ? (method === 'device' ? 'Uploading…' : 'Downloading…') : (method === 'device' ? 'Upload' : 'Download')}
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  )
}
