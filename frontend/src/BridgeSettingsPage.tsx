import { useCallback, useEffect, useState } from 'react'
import {
  Alert,
  Box,
  Button,
  Card,
  Checkbox,
  Divider,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControlLabel,
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
import { DeleteOutlineRounded, SettingsEthernetRounded } from '@mui/icons-material'
import { Link, useParams } from 'react-router-dom'

import {
  clearBridgeDHCPLeases,
  configureBridge,
  deleteBridgeDHCPLease,
  getNetworkingStatus,
  setBridgeDHCPStaticLease,
  type DHCPLease,
  type NetworkBridge,
  type NetworkingStatus,
} from './api'

const dnsLabelPattern = /^(?=.{1,63}$)[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?$/

function validDNSSuffix(value: string): boolean {
  const suffix = value.trim().replace(/.$/, '')
  return suffix.length > 0 && suffix.length <= 253 && suffix.split('.').every((label) => dnsLabelPattern.test(label))
}

function firstAvailableStaticAddress(cidr: string, poolStart: string, leases: DHCPLease[]): string {
  const [networkAddress] = cidr.split('/')
  const networkParts = networkAddress.split('.').map(Number)
  const poolParts = poolStart.split('.').map(Number)
  if (networkParts.length !== 4 || poolParts.length !== 4 || networkParts.some((part) => !Number.isInteger(part) || part < 0 || part > 255) || poolParts.some((part) => !Number.isInteger(part) || part < 0 || part > 255)) return ''
  const toNumber = (parts: number[]) => ((parts[0] << 24) >>> 0) + (parts[1] << 16) + (parts[2] << 8) + parts[3]
  const toAddress = (value: number) => [value >>> 24, (value >>> 16) & 255, (value >>> 8) & 255, value & 255].join('.')
  const used = new Set(leases.map((lease) => lease.address))
  const network = toNumber(networkParts)
  const pool = toNumber(poolParts)
  for (let candidate = network + 2; candidate < pool; candidate += 1) {
    const address = toAddress(candidate)
    if (!used.has(address)) return address
  }
  return ''
}

export default function BridgeSettingsPage({ refreshInterval }: { refreshInterval: number | null }) {
  const { bridgeName = '' } = useParams()
  const [status, setStatus] = useState<NetworkingStatus | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [description, setDescription] = useState('')
  const [enabled, setEnabled] = useState(false)
  const [cidr, setCIDR] = useState('192.168.100.0/24')
  const [natEnabled, setNATEnabled] = useState(false)
  const [dnsEnabled, setDNSEnabled] = useState(false)
  const [dnsForwarders, setDNSForwarders] = useState('')
  const [autoDNS, setAutoDNS] = useState(false)
  const [dnsSuffix, setDNSSuffix] = useState('')
  const [httpBootURL, setHTTPBootURL] = useState('')
  const [staticLease, setStaticLease] = useState<DHCPLease | null>(null)
  const [staticAddress, setStaticAddress] = useState('')
  const [loadedBridgeName, setLoadedBridgeName] = useState('')
  const bridge = status?.bridges?.find((candidate) => candidate.name === bridgeName)
  const leases = bridge?.dhcp?.leases ?? []
  const runningWorkloads = (bridge?.workloads ?? []).some((workload) => workload.running)
  const dnsSuffixValid = validDNSSuffix(dnsSuffix)

  const load = useCallback(async (showLoading = false) => {
    if (showLoading) setLoading(true)
    try {
      setStatus(await getNetworkingStatus())
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'Unable to load bridge settings')
    } finally {
      if (showLoading) setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load(true)
    if (refreshInterval === null) return
    const timer = window.setInterval(() => void load(), refreshInterval)
    return () => window.clearInterval(timer)
  }, [load, refreshInterval])

  useEffect(() => {
    if (!bridge || loadedBridgeName === bridgeName) return
    setDescription(bridge.description ?? '')
    setEnabled(Boolean(bridge.dhcp?.enabled))
    setCIDR(bridge.dhcp?.cidr || '192.168.100.0/24')
    setNATEnabled(Boolean(bridge.dhcp?.nat_enabled))
    setDNSEnabled(Boolean(bridge.dhcp?.dns_enabled))
    setDNSForwarders((bridge.dhcp?.dns_forwarders ?? []).join('\n'))
    setAutoDNS(Boolean(bridge.dhcp?.auto_dns))
    setDNSSuffix(bridge.dhcp?.dns_suffix || `${bridge.name.toLowerCase()}.internal`)
    setHTTPBootURL(bridge.dhcp?.http_boot_url ?? '')
    setLoadedBridgeName(bridgeName)
  }, [bridge, bridgeName, loadedBridgeName])

  const run = async (operation: () => Promise<NetworkingStatus>) => {
    setBusy(true)
    setError('')
    try {
      setStatus(await operation())
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'The lease operation failed')
    } finally {
      setBusy(false)
    }
  }

  const save = () => run(() => configureBridge({
    bridge_name: bridgeName,
    description: description.trim(),
    dhcp_enabled: enabled,
    cidr: cidr.trim(),
    nat_enabled: enabled && natEnabled,
    dns_enabled: enabled && dnsEnabled,
    dns_forwarders: dnsEnabled ? dnsForwarders.split(/[\s,]+/).filter(Boolean) : [],
    auto_dns: enabled && dnsEnabled && autoDNS,
    dns_suffix: autoDNS ? dnsSuffix.trim() : '',
    http_boot_url: enabled ? httpBootURL.trim() : '',
  }))

  const openStatic = (lease: DHCPLease) => {
    setStaticLease(lease)
    setStaticAddress(lease.is_static ? lease.address : firstAvailableStaticAddress(bridge?.dhcp?.cidr ?? '', bridge?.dhcp?.pool_start ?? '', leases))
  }

  if (!loading && !bridge) {
    return <Alert severity="error">Bridge {bridgeName || 'not found'} does not exist. <Button component={Link} to="/configuration/networking">Return to networking</Button></Alert>
  }

  return (
    <Box>
      <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2} sx={{ justifyContent: 'space-between', mb: 3.5 }}>
        <Box>
          <Typography variant="h4">Bridge settings</Typography>
          <Typography color="text.secondary" sx={{ mt: 0.75 }}>{bridgeName}</Typography>
        </Box>
        <Button component={Link} to="/configuration/networking">Back to networking</Button>
      </Stack>
      {error && <Alert severity="error" onClose={() => setError('')} sx={{ mb: 2.5 }}>{error}</Alert>}
      {runningWorkloads && <Alert severity="warning" sx={{ mb: 2.5 }}>Network service settings are locked while workloads are running. The bridge description can still be changed.</Alert>}

      <Card variant="outlined" sx={{ p: { xs: 2, sm: 3 }, mb: 3 }}>
        <Stack spacing={2}>
          <Typography variant="h6">Network services</Typography>
          <TextField fullWidth label="Description" value={description} onChange={(event) => setDescription(event.target.value)} helperText="Shown when selecting this bridge for a virtual machine" slotProps={{ htmlInput: { maxLength: 255 } }} />
          <FormControlLabel control={<Checkbox checked={enabled} disabled={runningWorkloads} onChange={(event) => {
            setEnabled(event.target.checked)
            if (!event.target.checked) { setNATEnabled(false); setDNSEnabled(false); setAutoDNS(false) }
          }} />} label="Enable App Runner DHCP for this bridge" />
          <TextField fullWidth label="IPv4 range (CIDR)" value={cidr} onChange={(event) => setCIDR(event.target.value)} disabled={!enabled || runningWorkloads} helperText="The bridge uses .1; dynamic leases begin at .50, leaving earlier addresses for static reservations." />
          <FormControlLabel control={<Checkbox checked={natEnabled} disabled={!enabled || runningWorkloads} onChange={(event) => setNATEnabled(event.target.checked)} />} label="Enable NAT and outbound routing for this range" />
          <Divider />
          <FormControlLabel control={<Checkbox checked={dnsEnabled} disabled={!enabled || runningWorkloads} onChange={(event) => { setDNSEnabled(event.target.checked); if (!event.target.checked) setAutoDNS(false) }} />} label="Enable managed DNS for this bridge" />
          <TextField fullWidth multiline minRows={2} label="Forwarding DNS servers" value={dnsForwarders} onChange={(event) => setDNSForwarders(event.target.value)} disabled={!enabled || !dnsEnabled || runningWorkloads} helperText="One IP address per line, or separate addresses with commas. An optional port is supported." />
          <FormControlLabel control={<Checkbox checked={autoDNS} disabled={!enabled || !dnsEnabled || runningWorkloads} onChange={(event) => setAutoDNS(event.target.checked)} />} label="Auto DNS for virtual machines on this bridge" />
          <TextField fullWidth label="Auto DNS suffix" value={dnsSuffix} onChange={(event) => setDNSSuffix(event.target.value)} disabled={!enabled || !dnsEnabled || !autoDNS || runningWorkloads} error={enabled && dnsEnabled && autoDNS && !dnsSuffixValid} helperText={dnsSuffixValid ? `VMs are published as VM-NAME.${dnsSuffix}` : 'Use one or more DNS labels separated by dots'} />
          <Divider />
          <TextField fullWidth label="UEFI HTTP boot URL" value={httpBootURL} onChange={(event) => setHTTPBootURL(event.target.value)} disabled={!enabled || runningWorkloads} placeholder="http://192.168.100.50/boot/bootx64.efi" helperText="Offered as the boot file to UEFI HTTP boot clients on this bridge. Leave empty to disable." />
          <Stack direction="row" sx={{ justifyContent: 'flex-end' }}><Button variant="contained" disabled={busy || (enabled && (!cidr.trim() || (dnsEnabled && (!dnsForwarders.trim() || (autoDNS && !dnsSuffixValid)))))} onClick={() => void save()}>Save settings</Button></Stack>
        </Stack>
      </Card>

      <Card variant="outlined" sx={{ p: { xs: 2, sm: 3 } }}>
        <Stack direction={{ xs: 'column', sm: 'row' }} spacing={1.5} sx={{ justifyContent: 'space-between', alignItems: { sm: 'center' }, mb: 2 }}>
          <Box><Typography variant="h6">DHCP leases</Typography><Typography variant="body2" color="text.secondary">Static reservations persist when dynamic leases are cleared.</Typography></Box>
          <Button color="warning" disabled={busy || !leases.some((lease) => !lease.is_static)} onClick={() => {
            if (window.confirm('Clear all dynamic DHCP leases on this bridge? Static reservations will remain.')) void run(() => clearBridgeDHCPLeases(bridgeName))
          }}>Clear leases</Button>
        </Stack>
        <TableContainer><Table size="small">
          <TableHead><TableRow><TableCell>Virtual machine</TableCell><TableCell>Address</TableCell><TableCell>Lease</TableCell><TableCell>MAC address</TableCell><TableCell align="right">Actions</TableCell></TableRow></TableHead>
          <TableBody>
            {leases.map((lease) => <TableRow key={lease.hardware_address} hover>
              <TableCell><Typography sx={{ fontWeight: 650 }}>{lease.vm_name || 'Unmanaged client'}</Typography></TableCell>
              <TableCell sx={{ fontFamily: 'monospace' }}>{lease.address}</TableCell>
              <TableCell>{lease.is_static ? 'Static reservation' : new Date(lease.expires_at).toLocaleString()}</TableCell>
              <TableCell sx={{ fontFamily: 'monospace' }}>{lease.hardware_address}</TableCell>
              <TableCell align="right">
                <Button size="small" disabled={busy} onClick={() => openStatic(lease)}>{lease.is_static ? 'Change static IP' : 'Make static'}</Button>
                <Tooltip title={lease.is_static ? 'Delete static reservation' : 'Delete lease'}><IconButton size="small" color="error" disabled={busy} aria-label={`Delete lease for ${lease.vm_name || lease.hardware_address}`} onClick={() => {
                  if (window.confirm(`Delete the ${lease.is_static ? 'static reservation' : 'DHCP lease'} for ${lease.vm_name || lease.hardware_address}?`)) void run(() => deleteBridgeDHCPLease(bridgeName, lease.hardware_address))
                }}><DeleteOutlineRounded fontSize="small" /></IconButton></Tooltip>
              </TableCell>
            </TableRow>)}
            {leases.length === 0 && <TableRow><TableCell colSpan={5} align="center" sx={{ py: 5 }}>No DHCP leases on this bridge.</TableCell></TableRow>}
          </TableBody>
        </Table></TableContainer>
      </Card>

      <Dialog open={Boolean(staticLease)} onClose={busy ? undefined : () => setStaticLease(null)} fullWidth maxWidth="xs">
        <DialogTitle>Set static IP</DialogTitle>
        <DialogContent>
          <Stack spacing={1.5} sx={{ mt: 1 }}>
            {staticLease && <Typography variant="body2" color="text.secondary">{staticLease.vm_name || staticLease.hardware_address}</Typography>}
            <TextField autoFocus label="Static IPv4 address" value={staticAddress} onChange={(event) => setStaticAddress(event.target.value)} helperText="Choose an unused address before the dynamic DHCP pool (for example .2 through .49)." />
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setStaticLease(null)} disabled={busy}>Cancel</Button>
          <Button variant="contained" disabled={busy || !staticAddress.trim()} onClick={() => void run(async () => {
            if (!staticLease) throw new Error('No lease selected')
            const updated = await setBridgeDHCPStaticLease(bridgeName, staticLease.hardware_address, staticAddress)
            setStaticLease(null)
            return updated
            })}>Save static IP</Button>
        </DialogActions>
      </Dialog>
    </Box>
  )
}
