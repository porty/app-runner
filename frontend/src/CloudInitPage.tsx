import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  Alert,
  Box,
  Button,
  Card,
  CircularProgress,
  Divider,
  List,
  ListItemButton,
  ListItemText,
  Stack,
  TextField,
  Typography,
} from '@mui/material'
import { AddRounded, DeleteOutlineRounded, SaveRounded } from '@mui/icons-material'

import {
  createCloudInitTemplate,
  deleteCloudInitTemplate,
  listCloudInitTemplates,
  updateCloudInitTemplate,
  type CloudInitTemplate,
} from './api'

function sortTemplates(templates: CloudInitTemplate[]): CloudInitTemplate[] {
  return [...templates].sort((left, right) => left.name.localeCompare(right.name, undefined, { sensitivity: 'base' }))
}

function formatUpdated(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '' : `Updated ${date.toLocaleString()}`
}

export default function CloudInitPage() {
  const [templates, setTemplates] = useState<CloudInitTemplate[]>([])
  const [selectedName, setSelectedName] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const [draftName, setDraftName] = useState('')
  const [draftUserData, setDraftUserData] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')

  const selectTemplate = useCallback((template: CloudInitTemplate) => {
    setSelectedName(template.name)
    setCreating(false)
    setDraftName(template.name)
    setDraftUserData(template.user_data)
    setError('')
    setSuccess('')
  }, [])

  useEffect(() => {
    void (async () => {
      try {
        const loaded = sortTemplates(await listCloudInitTemplates())
        setTemplates(loaded)
        if (loaded.length > 0) selectTemplate(loaded[0])
        else setCreating(true)
      } catch (requestError) {
        setError(requestError instanceof Error ? requestError.message : 'Cloud Init templates could not be loaded')
      } finally {
        setLoading(false)
      }
    })()
  }, [selectTemplate])

  const selected = useMemo(
    () => templates.find((template) => template.name === selectedName) ?? null,
    [selectedName, templates],
  )
  const trimmedName = draftName.trim()
  const duplicateName = templates.some((template) =>
    template.name.toLowerCase() === trimmedName.toLowerCase() && template.name !== selectedName,
  )
  const dirty = creating || Boolean(selected && (selected.name !== trimmedName || selected.user_data !== draftUserData))
  const valid = trimmedName.length > 0 && trimmedName.length <= 80 && !duplicateName && draftUserData.length <= 1048576

  const newTemplate = () => {
    setSelectedName(null)
    setCreating(true)
    setDraftName('')
    setDraftUserData('#cloud-config\n')
    setError('')
    setSuccess('')
  }

  const save = async () => {
    if (!valid) return
    setBusy(true)
    setError('')
    setSuccess('')
    try {
      const saved = creating
        ? await createCloudInitTemplate(trimmedName, draftUserData)
        : await updateCloudInitTemplate(selectedName ?? '', trimmedName, draftUserData)
      setTemplates((current) => sortTemplates([
        ...current.filter((template) => template.name !== selectedName),
        saved,
      ]))
      setSelectedName(saved.name)
      setCreating(false)
      setDraftName(saved.name)
      setDraftUserData(saved.user_data)
      setSuccess(creating ? `Created ${saved.name}.` : `Saved ${saved.name}.`)
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'The Cloud Init template could not be saved')
    } finally {
      setBusy(false)
    }
  }

  const remove = async () => {
    if (!selected || !window.confirm(`Delete the Cloud Init template “${selected.name}”?`)) return
    setBusy(true)
    setError('')
    setSuccess('')
    try {
      await deleteCloudInitTemplate(selected.name)
      const remaining = templates.filter((template) => template.name !== selected.name)
      setTemplates(remaining)
      if (remaining.length > 0) selectTemplate(remaining[0])
      else newTemplate()
      setSuccess(`Deleted ${selected.name}.`)
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'The Cloud Init template could not be deleted')
    } finally {
      setBusy(false)
    }
  }

  if (loading) return <Box sx={{ minHeight: 320, display: 'grid', placeItems: 'center' }}><CircularProgress /></Box>

  return (
    <Box>
      <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2} sx={{ justifyContent: 'space-between', alignItems: { sm: 'flex-start' }, mb: 3.5 }}>
        <Box>
          <Typography variant="h4">Cloud Init</Typography>
          <Typography color="text.secondary" sx={{ mt: 0.75 }}>
            Create reusable user-data templates for virtual machines.
          </Typography>
        </Box>
        <Button variant="contained" startIcon={<AddRounded />} onClick={newTemplate} disabled={busy}>New template</Button>
      </Stack>

      {error && <Alert severity="error" onClose={() => setError('')} sx={{ mb: 2.5 }}>{error}</Alert>}
      {success && <Alert severity="success" onClose={() => setSuccess('')} sx={{ mb: 2.5 }}>{success}</Alert>}

      <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', md: '280px minmax(0, 1fr)' }, gap: 2.5 }}>
        <Card variant="outlined" sx={{ alignSelf: 'start', overflow: 'hidden' }}>
          <Box sx={{ px: 2, py: 1.5 }}>
            <Typography sx={{ fontWeight: 750 }}>Templates</Typography>
            <Typography variant="caption" color="text.secondary">{templates.length} saved</Typography>
          </Box>
          <Divider />
          <List disablePadding>
            {templates.map((template) => (
              <ListItemButton key={template.name} selected={!creating && selectedName === template.name} onClick={() => selectTemplate(template)}>
                <ListItemText
                  primary={template.name}
                  secondary={formatUpdated(template.updated_at)}
                  slotProps={{ primary: { noWrap: true, sx: { fontWeight: 650 } }, secondary: { noWrap: true } }}
                />
              </ListItemButton>
            ))}
            {templates.length === 0 && (
              <Typography variant="body2" color="text.secondary" sx={{ px: 2, py: 3 }}>
                No templates saved yet.
              </Typography>
            )}
          </List>
        </Card>

        <Card variant="outlined" sx={{ p: { xs: 2, sm: 3 } }}>
          <Stack spacing={2.25}>
            <Box>
              <Typography variant="h6">{creating ? 'New template' : 'Edit template'}</Typography>
              <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
                Loading a template into a VM copies its contents. Existing VMs are not changed when this template is edited.
              </Typography>
            </Box>
            <TextField
              label="Template name"
              value={draftName}
              onChange={(event) => setDraftName(event.target.value)}
              error={duplicateName || draftName.length > 80}
              helperText={duplicateName ? 'A template with this name already exists.' : 'Maximum 80 characters.'}
              slotProps={{ htmlInput: { maxLength: 80 } }}
            />
            <TextField
              label="user-data"
              multiline
              minRows={18}
              value={draftUserData}
              onChange={(event) => setDraftUserData(event.target.value)}
              placeholder={'#cloud-config\npackages:\n  - curl'}
              helperText="Written verbatim to the VM’s NoCloud ISO. Maximum size: 1 MiB."
              sx={{ '& .MuiInputBase-input': { fontFamily: 'monospace' } }}
              slotProps={{ htmlInput: { maxLength: 1048576, spellCheck: false } }}
            />
            <Stack direction="row" spacing={1} sx={{ justifyContent: 'flex-end' }}>
              {!creating && <Button color="error" startIcon={<DeleteOutlineRounded />} onClick={() => void remove()} disabled={busy}>Delete</Button>}
              <Button variant="contained" startIcon={<SaveRounded />} onClick={() => void save()} disabled={busy || !valid || !dirty}>
                {busy ? 'Saving…' : creating ? 'Create template' : 'Save changes'}
              </Button>
            </Stack>
          </Stack>
        </Card>
      </Box>
    </Box>
  )
}
