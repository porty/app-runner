import { useEffect, useRef, useState } from 'react'
import { Alert, Box, Button, Chip, CircularProgress, Stack, Typography } from '@mui/material'
import { KeyboardRounded, RefreshRounded } from '@mui/icons-material'
import { Link, useParams } from 'react-router-dom'
import RFB from '@novnc/novnc'

import { getVM, type VirtualMachine } from './api'

export default function ConsolePage() {
  const { id = '' } = useParams()
  const target = useRef<HTMLDivElement | null>(null)
  const rfb = useRef<RFB | null>(null)
  const [vm, setVM] = useState<VirtualMachine | null>(null)
  const [connection, setConnection] = useState<'connecting' | 'connected' | 'disconnected'>('connecting')
  const [error, setError] = useState('')

  useEffect(() => {
    void getVM(id).then(setVM).catch((requestError: unknown) => {
      setError(requestError instanceof Error ? requestError.message : 'Unable to load the virtual machine')
    })
  }, [id])

  useEffect(() => {
    if (!vm || vm.status !== 'VM_STATUS_RUNNING' || !target.current) return
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const client = new RFB(target.current, `${protocol}//${window.location.host}${vm.console_path}`, { shared: true })
    client.scaleViewport = true
    client.resizeSession = false
    client.focusOnClick = true
    const connected = () => setConnection('connected')
    const disconnected = (event: Event) => {
      const clean = (event as CustomEvent<{ clean: boolean }>).detail?.clean
      setConnection('disconnected')
      if (!clean) setError('The console connection ended unexpectedly.')
    }
    client.addEventListener('connect', connected)
    client.addEventListener('disconnect', disconnected)
    rfb.current = client
    return () => {
      client.removeEventListener('connect', connected)
      client.removeEventListener('disconnect', disconnected)
      client.disconnect()
      rfb.current = null
    }
  }, [vm])

  if (error && !vm) return <Alert severity="error">{error}</Alert>
  if (!vm) return <Box sx={{ display: 'grid', placeItems: 'center', minHeight: 320 }}><CircularProgress /></Box>
  if (vm.status !== 'VM_STATUS_RUNNING') {
    return (
      <Alert severity="info" action={<Button component={Link} to="/compute/virtual-machines">Back to virtual machines</Button>}>
        Start {vm.name} before opening its console.
      </Alert>
    )
  }

  return (
    <Box>
      <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2} sx={{ justifyContent: 'space-between', alignItems: { sm: 'center' }, mb: 2 }}>
        <Box>
          <Typography variant="h4">{vm.name}</Typography>
          <Typography color="text.secondary">Interactive noVNC console</Typography>
        </Box>
        <Stack direction="row" spacing={1} sx={{ alignItems: 'center' }}>
          <Chip size="small" color={connection === 'connected' ? 'success' : 'default'} label={connection} />
          <Button variant="outlined" startIcon={<KeyboardRounded />} onClick={() => rfb.current?.sendCtrlAltDel()} disabled={connection !== 'connected'}>
            Ctrl+Alt+Del
          </Button>
          <Button component={Link} to="/compute/virtual-machines" startIcon={<RefreshRounded />}>VM list</Button>
        </Stack>
      </Stack>
      {error && <Alert severity="error" onClose={() => setError('')} sx={{ mb: 2 }}>{error}</Alert>}
      <Box
        ref={target}
        sx={{
          height: 'calc(100vh - 220px)',
          minHeight: 420,
          bgcolor: '#050505',
          border: 1,
          borderColor: 'divider',
          borderRadius: 2,
          overflow: 'hidden',
          '& canvas': { display: 'block' },
        }}
      />
    </Box>
  )
}
