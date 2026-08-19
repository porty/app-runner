import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  Alert,
  AlertTitle,
  Box,
  Button,
  Card,
  CardContent,
  Checkbox,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Divider,
  FormControlLabel,
  LinearProgress,
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
  CableRounded,
  CheckCircleRounded,
  DeleteOutlineRounded,
  ErrorOutlineRounded,
  HubRounded,
  LinkOffRounded,
  NetworkCheckRounded,
  RefreshRounded,
  WarningAmberRounded,
} from '@mui/icons-material'
import { Link } from 'react-router-dom'

import {
  applyNetworkChange,
  confirmNetworkChange,
  getNetworkingStatus,
  revertNetworkChange,
  type NetworkBridge,
  type NetworkChangeRequest,
  type NetworkDiagnostic,
  type NetworkingStatus,
  type PendingNetworkChange,
} from './api'

export default function NetworkingPage() {
  const [status, setStatus] = useState<NetworkingStatus | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [createOpen, setCreateOpen] = useState(false)
  const [bridgeName, setBridgeName] = useState('')
  const [attachBridge, setAttachBridge] = useState('')
  const [attachInterface, setAttachInterface] = useState('')
  const [migrateAddresses, setMigrateAddresses] = useState(true)

  const refresh = useCallback(async (showLoading = false) => {
    if (showLoading) setLoading(true)
    try {
      setStatus(await getNetworkingStatus())
      setError('')
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'Unable to inspect host networking')
    } finally {
      if (showLoading) setLoading(false)
    }
  }, [])

  useEffect(() => {
    void refresh(true)
    const timer = window.setInterval(() => void refresh(), 2000)
    return () => window.clearInterval(timer)
  }, [refresh])

  const apply = async (change: NetworkChangeRequest) => {
    setBusy(true)
    setError('')
    try {
      const pending = await applyNetworkChange(change)
      setStatus((current) => current ? { ...current, pending_change: pending } : current)
      setCreateOpen(false)
      setAttachBridge('')
      await refresh()
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'The network change failed')
    } finally {
      setBusy(false)
    }
  }

  const confirm = async (pending: PendingNetworkChange) => {
    setBusy(true)
    try {
      setStatus(await confirmNetworkChange(pending.id))
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'Unable to confirm the network change')
    } finally {
      setBusy(false)
    }
  }

  const revert = async (pending: PendingNetworkChange) => {
    setBusy(true)
    try {
      setStatus(await revertNetworkChange(pending.id))
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'Unable to revert the network change')
    } finally {
      setBusy(false)
    }
  }

  const bridges = status?.bridges ?? []
  const interfaces = status?.interfaces ?? []
  const attachableInterfaces = interfaces.filter((networkInterface) => networkInterface.can_attach)
  const mutationsDisabled = busy || Boolean(status?.pending_change) || !status?.can_manage

  return (
    <Box>
      <Stack direction={{ xs: 'column', md: 'row' }} spacing={2} sx={{ justifyContent: 'space-between', mb: 3.5 }}>
        <Box>
          <Typography variant="h4">Networking</Typography>
          <Typography color="text.secondary" sx={{ mt: 0.75 }}>
            Inspect Linux bridges, workload assignments, and the backend's effective permissions.
          </Typography>
        </Box>
        <Stack direction="row" spacing={1} sx={{ alignSelf: { md: 'center' } }}>
          <Button startIcon={<RefreshRounded />} onClick={() => void refresh(true)} disabled={loading}>Refresh</Button>
          <Button variant="contained" startIcon={<AddRounded />} onClick={() => setCreateOpen(true)} disabled={mutationsDisabled}>
            Create bridge
          </Button>
        </Stack>
      </Stack>

      {status?.pending_change && (
        <PendingChangeAlert pending={status.pending_change} busy={busy} onConfirm={confirm} onRevert={revert} />
      )}
      {error && <Alert severity="error" onClose={() => setError('')} sx={{ mb: 2.5 }}>{error}</Alert>}
      {status && !status.can_manage && (
        <Alert severity="warning" sx={{ mb: 2.5 }}>
          <AlertTitle>Read-only networking</AlertTitle>
          This backend lacks CAP_NET_ADMIN. Diagnostics are live, but bridge controls are disabled. See the permission check below for the exact remediation.
        </Alert>
      )}

      {status && <IdentitySummary status={status} />}

      <Typography variant="h5" sx={{ mt: 4, mb: 2 }}>Linux bridges</Typography>
      {bridges.length === 0 && !loading && (
        <Alert severity="info" sx={{ mb: 2.5 }}>
          No Linux bridges were detected. {status?.can_manage ? 'Create one here or configure one outside App Runner.' : 'Grant bridge modification permission or configure one outside App Runner.'}
        </Alert>
      )}
      <Stack spacing={2}>
        {bridges.map((bridge) => (
          <BridgeCard
            key={bridge.name}
            bridge={bridge}
            disabled={mutationsDisabled}
            canAttach={attachableInterfaces.length > 0}
            onApply={apply}
            onAttach={() => {
              setAttachBridge(bridge.name)
              setAttachInterface(attachableInterfaces[0]?.name ?? '')
            }}
          />
        ))}
      </Stack>

      <Typography variant="h5" sx={{ mt: 4, mb: 2 }}>Host interfaces</Typography>
      <TableContainer sx={{ border: 1, borderColor: 'divider', borderRadius: 2, bgcolor: 'background.paper' }}>
        <Table size="small">
          <TableHead><TableRow><TableCell>Interface</TableCell><TableCell>State</TableCell><TableCell>Master</TableCell><TableCell>Addresses</TableCell><TableCell>MTU</TableCell></TableRow></TableHead>
          <TableBody>
            {interfaces.map((networkInterface) => (
              <TableRow key={networkInterface.name} hover>
                <TableCell><Stack direction="row" spacing={1} sx={{ alignItems: 'center' }}><Typography sx={{ fontWeight: 650 }}>{networkInterface.name}</Typography>{networkInterface.is_bridge && <Chip label="bridge" size="small" />}</Stack></TableCell>
                <TableCell><Chip size="small" color={networkInterface.is_up ? 'success' : 'default'} label={networkInterface.is_up ? 'Up' : 'Down'} /></TableCell>
                <TableCell>{networkInterface.master || '—'}</TableCell>
                <TableCell>{(networkInterface.addresses ?? []).join(', ') || '—'}</TableCell>
                <TableCell>{networkInterface.mtu}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </TableContainer>

      <Typography variant="h5" sx={{ mt: 4, mb: 2 }}>Backend diagnostics</Typography>
      <DiagnosticTable diagnostics={status?.diagnostics ?? []} />

      <Dialog open={createOpen} onClose={() => setCreateOpen(false)} fullWidth maxWidth="xs">
        <DialogTitle>Create Linux bridge</DialogTitle>
        <DialogContent>
          <TextField fullWidth autoFocus label="Bridge name" value={bridgeName} onChange={(event) => setBridgeName(event.target.value)} helperText="1–15 letters, numbers, dots, underscores, or hyphens" sx={{ mt: 1 }} />
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setCreateOpen(false)}>Cancel</Button>
          <Button variant="contained" disabled={!bridgeName.trim() || busy} onClick={() => void apply({ type: 'NETWORK_CHANGE_TYPE_CREATE_BRIDGE', bridge_name: bridgeName.trim() })}>Create with rollback</Button>
        </DialogActions>
      </Dialog>

      <Dialog open={Boolean(attachBridge)} onClose={() => setAttachBridge('')} fullWidth maxWidth="sm">
        <DialogTitle>Attach interface to {attachBridge}</DialogTitle>
        <DialogContent>
          <Alert severity="warning" sx={{ mt: 1, mb: 2 }}>
            Attaching the interface carrying this browser's connection can interrupt connectivity. The backend will revert automatically unless you confirm within 15 seconds.
          </Alert>
          <TextField select fullWidth label="Host interface" value={attachInterface} onChange={(event) => setAttachInterface(event.target.value)}>
            {attachableInterfaces.map((networkInterface) => (
              <MenuItem key={networkInterface.name} value={networkInterface.name}>
                {networkInterface.name}{(networkInterface.addresses ?? []).length ? ` · ${(networkInterface.addresses ?? []).join(', ')}` : ''}
              </MenuItem>
            ))}
          </TextField>
          <FormControlLabel
            sx={{ mt: 1.5 }}
            control={<Checkbox checked={migrateAddresses} onChange={(event) => setMigrateAddresses(event.target.checked)} />}
            label="Move global IP addresses and non-kernel routes from the interface to the bridge"
          />
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setAttachBridge('')}>Cancel</Button>
          <Button
            variant="contained"
            disabled={!attachInterface || busy}
            onClick={() => void apply({
              type: 'NETWORK_CHANGE_TYPE_ATTACH_INTERFACE', bridge_name: attachBridge,
              interface_name: attachInterface, migrate_addresses: migrateAddresses,
            })}
          >
            Attach with rollback
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  )
}

function IdentitySummary({ status }: { status: NetworkingStatus }) {
  return (
    <Card variant="outlined">
      <CardContent>
        <Stack direction={{ xs: 'column', md: 'row' }} spacing={3} sx={{ alignItems: { md: 'center' } }}>
          <Box sx={{ flex: 1 }}>
            <Typography variant="overline" color="text.secondary">Backend identity</Typography>
            <Typography variant="h6">{status.user.username} <Typography component="span" color="text.secondary">UID {status.user.uid}</Typography></Typography>
            <Typography variant="body2" color="text.secondary">Groups: {(status.user.groups ?? []).join(', ') || 'none reported'}</Typography>
          </Box>
          <Chip icon={<NetworkCheckRounded />} color={status.can_manage ? 'success' : 'warning'} label={status.can_manage ? 'Can modify bridges' : 'Diagnostics only'} />
          <Chip color={status.user.has_cap_net_admin ? 'success' : 'default'} label={status.user.has_cap_net_admin ? 'CAP_NET_ADMIN present' : 'No CAP_NET_ADMIN'} />
        </Stack>
      </CardContent>
    </Card>
  )
}

function BridgeCard({ bridge, disabled, canAttach, onApply, onAttach }: {
  bridge: NetworkBridge
  disabled: boolean
  canAttach: boolean
  onApply: (change: NetworkChangeRequest) => Promise<void>
  onAttach: () => void
}) {
  const members = bridge.member_interfaces ?? []
  const workloads = bridge.workloads ?? []
  const deleteBridge = () => {
    if (!window.confirm(`Delete bridge ${bridge.name}? The change will still require confirmation.`)) return
    void onApply({ type: 'NETWORK_CHANGE_TYPE_DELETE_BRIDGE', bridge_name: bridge.name })
  }

  return (
    <Card variant="outlined">
      <CardContent sx={{ p: 3 }}>
        <Stack direction={{ xs: 'column', md: 'row' }} spacing={2} sx={{ justifyContent: 'space-between' }}>
          <Box>
            <Stack direction="row" spacing={1} sx={{ alignItems: 'center', flexWrap: 'wrap' }}>
              <HubRounded color="primary" />
              <Typography variant="h6">{bridge.name}</Typography>
              <Chip size="small" color={bridge.is_up ? 'success' : 'default'} label={bridge.is_up ? 'Up' : 'Down'} />
              <Chip size="small" color={bridge.usable_by_qemu ? 'success' : 'warning'} label={bridge.usable_by_qemu ? 'QEMU ready' : 'Diagnostics required'} />
            </Stack>
            <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
              MTU {bridge.mtu} · {bridge.hardware_address || 'no hardware address'} · {(bridge.addresses ?? []).join(', ') || 'no addresses'}
            </Typography>
          </Box>
          <Stack direction="row" spacing={1} sx={{ flexWrap: 'wrap', alignContent: 'flex-start' }}>
            <Button size="small" disabled={disabled} onClick={() => void onApply({ type: bridge.is_up ? 'NETWORK_CHANGE_TYPE_SET_BRIDGE_DOWN' : 'NETWORK_CHANGE_TYPE_SET_BRIDGE_UP', bridge_name: bridge.name })}>
              Bring {bridge.is_up ? 'down' : 'up'}
            </Button>
            <Button size="small" startIcon={<CableRounded />} disabled={disabled || !canAttach} onClick={onAttach}>Attach interface</Button>
            <Button size="small" color="error" startIcon={<DeleteOutlineRounded />} disabled={disabled || members.length > 0 || workloads.length > 0} onClick={deleteBridge}>Delete</Button>
          </Stack>
        </Stack>

        <Divider sx={{ my: 2.5 }} />
        <Stack direction={{ xs: 'column', lg: 'row' }} spacing={3}>
          <Box sx={{ flex: 1 }}>
            <Typography variant="subtitle2" sx={{ mb: 1 }}>Member interfaces</Typography>
            {members.length === 0 && <Typography variant="body2" color="text.secondary">No member interfaces.</Typography>}
            <Stack spacing={1}>
              {members.map((member) => (
                <Stack key={member} direction="row" spacing={1} sx={{ alignItems: 'center', justifyContent: 'space-between' }}>
                  <Box>
                    <Chip size="small" label={member} />
                    <Typography component="span" variant="caption" color="text.secondary" sx={{ ml: 1 }}>unmanaged or dynamically assigned interface</Typography>
                  </Box>
                  <Button size="small" color="warning" startIcon={<LinkOffRounded />} disabled={disabled} onClick={() => void onApply({ type: 'NETWORK_CHANGE_TYPE_DETACH_INTERFACE', bridge_name: bridge.name, interface_name: member })}>Detach</Button>
                </Stack>
              ))}
            </Stack>
          </Box>
          <Box sx={{ flex: 1 }}>
            <Typography variant="subtitle2" sx={{ mb: 1 }}>Managed workloads</Typography>
            {workloads.length === 0 && <Typography variant="body2" color="text.secondary">No App Runner VMs or containers are assigned.</Typography>}
            <Stack direction="row" spacing={1} sx={{ flexWrap: 'wrap' }}>
              {workloads.map((workload) => (
                <Chip
                  key={`${workload.workload_type}-${workload.id}`}
                  component={workload.workload_type === 'virtual_machine' ? Link : 'div'}
                  to={workload.workload_type === 'virtual_machine' ? '/compute/virtual-machines' : undefined}
                  clickable={workload.workload_type === 'virtual_machine'}
                  color={workload.running ? 'success' : 'default'}
                  label={`${workload.name} · ${workload.workload_type.replace('_', ' ')}`}
                />
              ))}
            </Stack>
          </Box>
        </Stack>

        <Box sx={{ mt: 2.5 }}><DiagnosticList diagnostics={bridge.diagnostics ?? []} compact /></Box>
      </CardContent>
    </Card>
  )
}

function DiagnosticList({ diagnostics, compact = false }: { diagnostics: NetworkDiagnostic[]; compact?: boolean }) {
  if (diagnostics.length === 0) return <Typography color="text.secondary">No diagnostics reported.</Typography>
  return (
    <Stack spacing={1}>
      {diagnostics.map((diagnostic) => {
        const severity = diagnostic.status === 'DIAGNOSTIC_STATUS_FAIL' ? 'error' : diagnostic.status === 'DIAGNOSTIC_STATUS_WARNING' ? 'warning' : diagnostic.status === 'DIAGNOSTIC_STATUS_PASS' ? 'success' : 'info'
        const icon = severity === 'error' ? <ErrorOutlineRounded /> : severity === 'warning' ? <WarningAmberRounded /> : severity === 'success' ? <CheckCircleRounded /> : undefined
        return (
          <Alert key={diagnostic.key} severity={severity} icon={icon} variant={compact ? 'outlined' : 'standard'}>
            <AlertTitle>{diagnostic.label}</AlertTitle>
            <DiagnosticText text={diagnostic.detail} />
            {diagnostic.remediation && <Box sx={{ mt: 1 }}><DiagnosticText text={diagnostic.remediation} /></Box>}
          </Alert>
        )
      })}
    </Stack>
  )
}

function DiagnosticTable({ diagnostics }: { diagnostics: NetworkDiagnostic[] }) {
  if (diagnostics.length === 0) return <Typography color="text.secondary">No diagnostics reported.</Typography>

  return (
    <TableContainer sx={{ border: 1, borderColor: 'divider', borderRadius: 2, bgcolor: 'background.paper' }}>
      <Table size="small">
        <TableHead>
          <TableRow>
            <TableCell>Status</TableCell>
            <TableCell>Check</TableCell>
            <TableCell>Diagnostic result</TableCell>
            <TableCell>Remediation</TableCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {diagnostics.map((diagnostic) => {
            const presentation = diagnostic.status === 'DIAGNOSTIC_STATUS_FAIL'
              ? { color: 'error' as const, icon: <ErrorOutlineRounded />, label: 'Failed' }
              : diagnostic.status === 'DIAGNOSTIC_STATUS_WARNING'
                ? { color: 'warning' as const, icon: <WarningAmberRounded />, label: 'Warning' }
                : diagnostic.status === 'DIAGNOSTIC_STATUS_PASS'
                  ? { color: 'success' as const, icon: <CheckCircleRounded />, label: 'Passed' }
                  : { color: 'default' as const, icon: undefined, label: 'Unknown' }

            return (
              <TableRow key={diagnostic.key} hover>
                <TableCell sx={{ verticalAlign: 'top', whiteSpace: 'nowrap' }}>
                  <Chip size="small" color={presentation.color} icon={presentation.icon} label={presentation.label} />
                </TableCell>
                <TableCell sx={{ verticalAlign: 'top', fontWeight: 650 }}>{diagnostic.label}</TableCell>
                <TableCell sx={{ verticalAlign: 'top' }}><DiagnosticText text={diagnostic.detail} /></TableCell>
                <TableCell sx={{ verticalAlign: 'top' }}>
                  {diagnostic.remediation ? <DiagnosticText text={diagnostic.remediation} /> : <Typography component="span" color="text.secondary">—</Typography>}
                </TableCell>
              </TableRow>
            )
          })}
        </TableBody>
      </Table>
    </TableContainer>
  )
}

function DiagnosticText({ text }: { text: string }) {
  const segments = splitDiagnosticText(text)
  return (
    <Stack spacing={0.75}>
      {segments.map((segment, index) => {
        if (segment.terminal) {
          return (
            <Box
              key={`${index}-${segment.text}`}
              component="pre"
              aria-label="Terminal command"
              sx={{
                m: 0,
                px: 1,
                py: 0.75,
                borderRadius: 1,
                bgcolor: 'grey.900',
                color: 'grey.100',
                fontFamily: 'monospace',
                fontSize: '0.78rem',
                lineHeight: 1.5,
                overflowX: 'auto',
                whiteSpace: 'pre-wrap',
              }}
            >
              {segment.text}
            </Box>
          )
        }
        return <Typography key={`${index}-${segment.text}`} variant="body2">{formatInlineCode(segment.text)}</Typography>
      })}
    </Stack>
  )
}

function splitDiagnosticText(text: string): Array<{ text: string; terminal: boolean }> {
  const segments: Array<{ text: string; terminal: boolean }> = []
  for (const rawLine of text.split('\n')) {
    const line = rawLine.trim()
    if (!line) continue
    if (line.startsWith('$ ') || line.startsWith('allow ') || line.startsWith('deny ')) {
      segments.push({ text: line, terminal: true })
      continue
    }

    // Older backends returned this shell command inline after the remediation prose.
    const commandIndex = line.indexOf('sudo setcap ')
    if (commandIndex >= 0) {
      const prose = line.slice(0, commandIndex).trimEnd()
      if (prose) segments.push({ text: prose, terminal: false })
      segments.push({ text: `$ ${line.slice(commandIndex).replace(/\.$/, '')}`, terminal: true })
      continue
    }
    segments.push({ text: line, terminal: false })
  }
  return segments
}

function formatInlineCode(text: string) {
  const normalized = text.replace(/'((?:allow|deny)\s+[^']+)'/g, '`$1`')
  const codePattern = /(`[^`]+`|(?:\.{0,2}\/|\/)(?:[\w.-]+\/)*[\w.-]+|\bCAP_NET_ADMIN\b|\b(?:executable|setuid|file-capabilities)=(?:true|false)\b|\b[\w.-]+:[\w.-]+\b|\b0?[0-7]{3,4}\b)/g
  return normalized.split(codePattern).filter(Boolean).map((part, index) => {
    const explicitlyMarked = part.startsWith('`') && part.endsWith('`')
    if (explicitlyMarked || codePattern.test(part)) {
      codePattern.lastIndex = 0
      return (
        <Box
          key={`${index}-${part}`}
          component="code"
          sx={{ px: 0.5, py: 0.125, borderRadius: 0.75, bgcolor: 'action.hover', fontFamily: 'monospace', fontSize: '0.85em' }}
        >
          {explicitlyMarked ? part.slice(1, -1) : part}
        </Box>
      )
    }
    codePattern.lastIndex = 0
    return part
  })
}

function PendingChangeAlert({ pending, busy, onConfirm, onRevert }: {
  pending: PendingNetworkChange
  busy: boolean
  onConfirm: (pending: PendingNetworkChange) => Promise<void>
  onRevert: (pending: PendingNetworkChange) => Promise<void>
}) {
  const [now, setNow] = useState(Date.now())
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 200)
    return () => window.clearInterval(timer)
  }, [])
  const remaining = useMemo(() => Math.max(0, Math.ceil((Date.parse(pending.expires_at) - now) / 1000)), [now, pending.expires_at])
  const progress = Math.max(0, Math.min(100, (remaining / 15) * 100))

  return (
    <Alert
      severity="warning"
      icon={<WarningAmberRounded />}
      sx={{ mb: 2.5 }}
      action={
        <Stack direction="row" spacing={1}>
          <Button color="inherit" variant="contained" disabled={busy || remaining === 0} onClick={() => void onConfirm(pending)}>Keep changes</Button>
          <Button color="inherit" disabled={busy} onClick={() => void onRevert(pending)}>Revert now</Button>
        </Stack>
      }
    >
      <AlertTitle>{pending.description}</AlertTitle>
      Backend rollback in {remaining} second{remaining === 1 ? '' : 's'} unless this browser confirms connectivity.
      <LinearProgress color="warning" variant="determinate" value={progress} sx={{ mt: 1, maxWidth: 420 }} />
    </Alert>
  )
}
