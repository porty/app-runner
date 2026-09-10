import { useEffect, useRef, useState, type MouseEvent as ReactMouseEvent } from 'react'
import { Alert, Box, Button, Chip, CircularProgress, ListItemIcon, ListItemText, Menu, MenuItem, Stack, ToggleButton, ToggleButtonGroup, Typography } from '@mui/material'
import { ArrowDropDownRounded, ClearAllRounded, DesktopWindowsRounded, KeyboardRounded, PlayArrowRounded, PowerOffRounded, PowerSettingsNewRounded, RefreshRounded, RestartAltRounded, TerminalRounded } from '@mui/icons-material'
import { Link, useParams } from 'react-router-dom'
import RFB from '@novnc/novnc'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'

import { getVM, rebootVM, startVM, stopVM, type VirtualMachine } from './api'
import { canAccessVMConsole } from './vmConsoleAccess'

export default function ConsolePage() {
  const { id = '' } = useParams()
  const target = useRef<HTMLDivElement | null>(null)
  const rfb = useRef<RFB | null>(null)
  const terminal = useRef<Terminal | null>(null)
  const expectedDisconnect = useRef(false)
  const [vm, setVM] = useState<VirtualMachine | null>(null)
  const [connection, setConnection] = useState<'connecting' | 'connected' | 'disconnected'>('connecting')
  const [error, setError] = useState('')
  const [starting, setStarting] = useState(false)
  const [powerMenuAnchor, setPowerMenuAnchor] = useState<HTMLElement | null>(null)
  const [powerBusy, setPowerBusy] = useState<'shutdown' | 'force-off' | 'reboot' | null>(null)
  const [consoleKind, setConsoleKind] = useState<'display' | 'serial'>('display')
  const consoleAvailable = vm ? canAccessVMConsole(vm.status) : false

  const start = async () => {
    setStarting(true)
    setError('')
    try {
      expectedDisconnect.current = false
      setConnection('connecting')
      setVM(await startVM(id))
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'Unable to start the virtual machine')
    } finally {
      setStarting(false)
    }
  }

  useEffect(() => {
    void getVM(id).then(setVM).catch((requestError: unknown) => {
      setError(requestError instanceof Error ? requestError.message : 'Unable to load the virtual machine')
    })
  }, [id])

  useEffect(() => {
    if (vm?.status !== 'VM_STATUS_STOPPING') return
    const timer = window.setInterval(() => {
      void getVM(id).then(setVM).catch((requestError: unknown) => {
        setError(requestError instanceof Error ? requestError.message : 'Unable to refresh the virtual machine status')
      })
    }, 1000)
    return () => window.clearInterval(timer)
  }, [id, vm?.status])

  useEffect(() => {
    if (!vm || !consoleAvailable || consoleKind !== 'display' || !target.current) return
    setConnection('connecting')
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const client = new RFB(target.current, `${protocol}//${window.location.host}${vm.console_path}`, { shared: true })
    client.scaleViewport = true
    client.resizeSession = false
    client.focusOnClick = true
    const connected = () => {
      expectedDisconnect.current = false
      setConnection('connected')
    }
    const disconnected = (event: Event) => {
      const clean = (event as CustomEvent<{ clean: boolean }>).detail?.clean
      setConnection('disconnected')
      if (!clean && !expectedDisconnect.current) setError('The console connection ended unexpectedly.')
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
  }, [consoleAvailable, consoleKind, vm?.console_path])

  useEffect(() => {
    if (!vm || !consoleAvailable || consoleKind !== 'serial' || !target.current) return
    setConnection('connecting')
    const instance = new Terminal({
      cursorBlink: true,
      scrollback: 10_000,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
      fontSize: 14,
      theme: { background: '#050505' },
    })
    const fit = new FitAddon()
    instance.loadAddon(fit)
    instance.open(target.current)
    fit.fit()
    terminal.current = instance

    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const socket = new WebSocket(`${protocol}//${window.location.host}${vm.serial_console_path}`)
    socket.binaryType = 'arraybuffer'
    let opened = false
    const input = instance.onData((data) => {
      if (socket.readyState === WebSocket.OPEN) socket.send(new TextEncoder().encode(data))
    })
    socket.onopen = () => {
      opened = true
      expectedDisconnect.current = false
      setConnection('connected')
      instance.focus()
    }
    socket.onmessage = (event) => {
      if (event.data instanceof ArrayBuffer) instance.write(new Uint8Array(event.data))
    }
    socket.onclose = () => {
      setConnection('disconnected')
      if (!expectedDisconnect.current) {
        setError(opened ? 'The serial console connection ended unexpectedly.' : 'The serial console could not be opened.')
      }
    }
    const resize = new ResizeObserver(() => fit.fit())
    resize.observe(target.current)
    return () => {
      resize.disconnect()
      input.dispose()
      socket.onclose = null
      socket.close()
      instance.dispose()
      terminal.current = null
    }
  }, [consoleAvailable, consoleKind, vm?.serial_console_path])

  const openPowerMenu = (event: ReactMouseEvent<HTMLButtonElement>) => {
    setPowerMenuAnchor(event.currentTarget)
  }

  const runPowerAction = async (action: 'shutdown' | 'force-off' | 'reboot') => {
    setPowerMenuAnchor(null)
    if (action === 'force-off' && !window.confirm(`Force ${vm?.name ?? 'this virtual machine'} off? Unsaved guest data may be lost.`)) return
    if (action === 'reboot' && !window.confirm(`Reboot ${vm?.name ?? 'this virtual machine'} now?`)) return
    setPowerBusy(action)
    setError('')
    expectedDisconnect.current = true
    try {
      if (action === 'shutdown') setVM(await stopVM(id))
      if (action === 'force-off') setVM(await stopVM(id, true))
      if (action === 'reboot') {
        setVM(await rebootVM(id))
        window.setTimeout(() => { expectedDisconnect.current = false }, 5000)
      }
    } catch (requestError) {
      expectedDisconnect.current = false
      setError(requestError instanceof Error ? requestError.message : 'The power operation failed')
    } finally {
      setPowerBusy(null)
    }
  }

  if (error && !vm) return <Alert severity="error">{error}</Alert>
  if (!vm) return <Box sx={{ display: 'grid', placeItems: 'center', minHeight: 320 }}><CircularProgress /></Box>
  if (vm.status === 'VM_STATUS_STOPPED') {
    return (
      <Stack spacing={2}>
        {error && <Alert severity="error" onClose={() => setError('')}>{error}</Alert>}
        <Alert
          severity="info"
          action={
            <Button color="inherit" startIcon={<PlayArrowRounded />} disabled={starting} onClick={() => void start()}>
              {starting ? 'Starting…' : 'Start'}
            </Button>
          }
        >
          {vm.name} is currently off.
        </Alert>
      </Stack>
    )
  }
  if (!canAccessVMConsole(vm.status)) {
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
          <Typography color="text.secondary">Interactive {consoleKind === 'display' ? 'display' : 'serial'} console</Typography>
        </Box>
        <Stack direction="row" spacing={1} sx={{ alignItems: 'center' }}>
          <ToggleButtonGroup
            size="small"
            exclusive
            value={consoleKind}
            onChange={(_, value: 'display' | 'serial' | null) => { if (value) setConsoleKind(value) }}
            aria-label="Console type"
          >
            <ToggleButton value="display" aria-label="Display console"><DesktopWindowsRounded fontSize="small" sx={{ mr: 0.75 }} />Display</ToggleButton>
            <ToggleButton value="serial" aria-label="Serial console"><TerminalRounded fontSize="small" sx={{ mr: 0.75 }} />Serial</ToggleButton>
          </ToggleButtonGroup>
          <Chip size="small" color={connection === 'connected' ? 'success' : 'default'} label={connection} />
          {consoleKind === 'display' ? (
            <Button variant="outlined" startIcon={<KeyboardRounded />} onClick={() => rfb.current?.sendCtrlAltDel()} disabled={connection !== 'connected'}>
              Ctrl+Alt+Del
            </Button>
          ) : (
            <Button variant="outlined" startIcon={<ClearAllRounded />} onClick={() => terminal.current?.clear()} disabled={connection !== 'connected'}>
              Clear
            </Button>
          )}
          <Button
            variant="outlined"
            color="inherit"
            startIcon={<PowerSettingsNewRounded />}
            endIcon={<ArrowDropDownRounded />}
            onClick={openPowerMenu}
            disabled={powerBusy !== null}
            aria-controls={powerMenuAnchor ? 'console-power-menu' : undefined}
            aria-haspopup="menu"
            aria-expanded={powerMenuAnchor ? 'true' : undefined}
          >
            {powerBusy ? 'Working…' : 'Power'}
          </Button>
          <Menu id="console-power-menu" anchorEl={powerMenuAnchor} open={Boolean(powerMenuAnchor)} onClose={() => setPowerMenuAnchor(null)}>
            <MenuItem onClick={() => void runPowerAction('shutdown')} disabled={vm.status !== 'VM_STATUS_RUNNING'}>
              <ListItemIcon><PowerSettingsNewRounded fontSize="small" /></ListItemIcon>
              <ListItemText primary="Shut down" secondary="Request a graceful shutdown" />
            </MenuItem>
            <MenuItem onClick={() => void runPowerAction('reboot')} disabled={vm.status !== 'VM_STATUS_RUNNING'}>
              <ListItemIcon><RestartAltRounded fontSize="small" /></ListItemIcon>
              <ListItemText primary="Reboot" secondary="Reset the running virtual machine" />
            </MenuItem>
            <MenuItem onClick={() => void runPowerAction('force-off')} sx={{ color: 'error.main' }}>
              <ListItemIcon sx={{ color: 'error.main' }}><PowerOffRounded fontSize="small" /></ListItemIcon>
              <ListItemText primary="Force off" secondary="Power off immediately" />
            </MenuItem>
          </Menu>
          <Button component={Link} to="/compute/virtual-machines" startIcon={<RefreshRounded />}>VM list</Button>
        </Stack>
      </Stack>
      {error && <Alert severity="error" onClose={() => setError('')} sx={{ mb: 2 }}>{error}</Alert>}
      <Box
        key={consoleKind}
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
          '& .xterm': { height: '100%', p: 1 },
        }}
      />
    </Box>
  )
}
