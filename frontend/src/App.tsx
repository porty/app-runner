import { lazy, Suspense, useState, type ReactNode } from 'react'
import {
  AppBar,
  Box,
  Button,
  Card,
  CardContent,
  CircularProgress,
  Collapse,
  Divider,
  Drawer,
  IconButton,
  List,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  MenuItem,
  Stack,
  TextField,
  Toolbar,
  Tooltip,
  Typography,
} from '@mui/material'
import {
  AppsRounded,
  Brightness4Rounded,
  Brightness7Rounded,
  ChevronLeftRounded,
  ChevronRightRounded,
  CloudSyncRounded,
  CloudQueueRounded,
  DashboardRounded,
  ExpandLessRounded,
  ExpandMoreRounded,
  Inventory2Rounded,
  LanRounded,
  MemoryRounded,
  PlayArrowRounded,
  SettingsRounded,
  StorageRounded,
  TuneRounded,
} from '@mui/icons-material'
import { Navigate, NavLink, Route, Routes, useLocation } from 'react-router-dom'

import CloudInitPage from './CloudInitPage'
import BridgeSettingsPage from './BridgeSettingsPage'
import CreateVirtualMachinePage from './CreateVirtualMachinePage'
import HostWarnings from './HostWarnings'
import NetworkingPage from './NetworkingPage'
import StoragePage from './StoragePage'
import VirtualMachineSettingsPage from './VirtualMachineSettingsPage'
import VirtualMachinesPage from './VirtualMachinesPage'
import { refreshIntervals, type RefreshSpeed } from './refreshSettings'

const ConsolePage = lazy(() => import('./ConsolePage'))

const expandedWidth = 272
const collapsedWidth = 76

interface AppProps {
  mode: 'light' | 'dark'
  onToggleMode: () => void
  refreshSpeed: RefreshSpeed
  onRefreshSpeedChange: (speed: RefreshSpeed) => void
}

interface NavigationGroup {
  label: string
  icon: ReactNode
  children: Array<{ label: string; path: string; icon: ReactNode; disabled?: boolean }>
}

const navigation: NavigationGroup[] = [
  {
    label: 'Workspace',
    icon: <DashboardRounded />,
    children: [
      { label: 'Overview', path: '/', icon: <AppsRounded /> },
      { label: 'Activity', path: '/activity', icon: <PlayArrowRounded />, disabled: true },
    ],
  },
  {
    label: 'Compute',
    icon: <MemoryRounded />,
    children: [
      { label: 'Virtual machines', path: '/compute/virtual-machines', icon: <CloudQueueRounded /> },
      { label: 'Containers', path: '/compute/containers', icon: <Inventory2Rounded />, disabled: true },
    ],
  },
  {
    label: 'Configuration',
    icon: <SettingsRounded />,
    children: [
      { label: 'Storage', path: '/configuration/storage', icon: <StorageRounded /> },
      { label: 'Cloud Init', path: '/configuration/cloud-init', icon: <CloudSyncRounded /> },
      { label: 'Networking', path: '/configuration/networking', icon: <LanRounded /> },
      { label: 'Preferences', path: '/configuration/preferences', icon: <TuneRounded />, disabled: true },
    ],
  },
]

function Sidebar({ expanded, onToggle }: { expanded: boolean; onToggle: () => void }) {
  const location = useLocation()
  const [openGroups, setOpenGroups] = useState<Record<string, boolean>>({
    Workspace: true,
    Compute: true,
    Configuration: true,
  })
  const width = expanded ? expandedWidth : collapsedWidth

  return (
    <Drawer
      variant="permanent"
      sx={{
        width,
        flexShrink: 0,
        '& .MuiDrawer-paper': {
          width,
          overflowX: 'hidden',
          borderRightColor: 'divider',
          transition: (theme) => theme.transitions.create('width'),
        },
      }}
    >
      <Toolbar sx={{ minHeight: 72, px: expanded ? 2.5 : 1.5, gap: 1.5 }}>
        <Box
          sx={{
            width: 38,
            height: 38,
            borderRadius: 2.5,
            display: 'grid',
            placeItems: 'center',
            color: 'primary.contrastText',
            bgcolor: 'primary.main',
            flexShrink: 0,
          }}
        >
          <PlayArrowRounded />
        </Box>
        {expanded && (
          <Box sx={{ minWidth: 0 }}>
            <Typography variant="h6" noWrap>
              App Runner
            </Typography>
            <Typography variant="caption" color="text.secondary" noWrap>
              Infrastructure console
            </Typography>
          </Box>
        )}
      </Toolbar>
      <Divider />
      <List sx={{ px: 1, py: 1.5 }}>
        {navigation.map((group) => {
          const open = openGroups[group.label]
          return (
            <Box key={group.label} sx={{ mb: 0.5 }}>
              <Tooltip title={expanded ? '' : group.label} placement="right">
                <ListItemButton
                  onClick={() =>
                    expanded &&
                    setOpenGroups((groups) => ({ ...groups, [group.label]: !groups[group.label] }))
                  }
                  sx={{ minHeight: 44, px: 1.5, borderRadius: 2 }}
                >
                  <ListItemIcon sx={{ minWidth: 0, mr: expanded ? 1.5 : 'auto', justifyContent: 'center' }}>
                    {group.icon}
                  </ListItemIcon>
                  {expanded && (
                    <ListItemText primary={group.label} slotProps={{ primary: { sx: { fontWeight: 650 } } }} />
                  )}
                  {expanded && (open ? <ExpandLessRounded /> : <ExpandMoreRounded />)}
                </ListItemButton>
              </Tooltip>
              <Collapse in={expanded && open} timeout="auto" unmountOnExit>
                <List component="div" disablePadding>
                  {group.children.map((item) => (
                    <ListItemButton
                      key={item.path}
                      component={NavLink}
                      to={item.path}
                      disabled={item.disabled}
                      selected={location.pathname === item.path || location.pathname.startsWith(`${item.path}/`)}
                      sx={{ minHeight: 40, pl: 3.25, borderRadius: 2, my: 0.25 }}
                    >
                      <ListItemIcon sx={{ minWidth: 36 }}>{item.icon}</ListItemIcon>
                      <ListItemText primary={item.label} secondary={item.disabled ? 'Coming later' : undefined} slotProps={{ primary: { variant: 'body2' }, secondary: { variant: 'caption' } }} />
                    </ListItemButton>
                  ))}
                </List>
              </Collapse>
            </Box>
          )
        })}
      </List>
      <Box sx={{ mt: 'auto', p: 1 }}>
        <Tooltip title={expanded ? 'Collapse navigation' : 'Expand navigation'} placement="right">
          <ListItemButton onClick={onToggle} sx={{ borderRadius: 2, minHeight: 44, px: 1.5 }}>
            <ListItemIcon sx={{ minWidth: 0, mr: expanded ? 1.5 : 'auto', justifyContent: 'center' }}>
              {expanded ? <ChevronLeftRounded /> : <ChevronRightRounded />}
            </ListItemIcon>
            {expanded && <ListItemText primary="Collapse navigation" />}
          </ListItemButton>
        </Tooltip>
      </Box>
    </Drawer>
  )
}

function Overview() {
  const destinations = [
    { label: 'Virtual machines', description: 'Create, start, and manage QEMU/KVM workloads.', path: '/compute/virtual-machines', icon: <CloudQueueRounded /> },
    { label: 'Networking', description: 'Inspect bridges, diagnostics, and managed guest network services.', path: '/configuration/networking', icon: <LanRounded /> },
    { label: 'Storage', description: 'Browse ISO images, cloud images, and virtual machine disks.', path: '/configuration/storage', icon: <StorageRounded /> },
    { label: 'Cloud Init', description: 'Maintain reusable first-boot user-data templates.', path: '/configuration/cloud-init', icon: <CloudSyncRounded /> },
  ]

  return (
    <Page title="Overview" description="A local control plane for virtual machines on this host.">
      <Stack direction={{ xs: 'column', lg: 'row' }} spacing={2.5} sx={{ flexWrap: 'wrap' }}>
        {destinations.map((destination) => (
          <Card key={destination.path} variant="outlined" sx={{ flex: '1 1 260px' }}>
            <CardContent sx={{ p: 3 }}>
              <Stack direction="row" spacing={1.5} sx={{ alignItems: 'center', mb: 1 }}>
                {destination.icon}
                <Typography variant="h6">{destination.label}</Typography>
              </Stack>
              <Typography color="text.secondary" sx={{ mb: 2.5 }}>{destination.description}</Typography>
              <Button component={NavLink} to={destination.path} variant="outlined">Open</Button>
            </CardContent>
          </Card>
        ))}
      </Stack>
    </Page>
  )
}

function Page({ title, description, children }: { title: string; description: string; children: ReactNode }) {
  return (
    <Box>
      <Typography variant="h4">{title}</Typography>
      <Typography color="text.secondary" sx={{ mt: 0.75, mb: 3.5 }}>
        {description}
      </Typography>
      {children}
    </Box>
  )
}

function PlaceholderPage({ title, description }: { title: string; description: string }) {
  return (
    <Page title={title} description={description}>
      <Card variant="outlined">
        <CardContent sx={{ minHeight: 220, display: 'grid', placeItems: 'center', textAlign: 'center' }}>
          <Box>
            <Typography variant="h6">Ready for the next milestone</Typography>
            <Typography color="text.secondary" sx={{ mt: 1 }}>
              This navigation destination is in place and will gain management controls as the backend grows.
            </Typography>
          </Box>
        </CardContent>
      </Card>
    </Page>
  )
}

export default function App({ mode, onToggleMode, refreshSpeed, onRefreshSpeedChange }: AppProps) {
  const [sidebarExpanded, setSidebarExpanded] = useState(true)
  const drawerWidth = sidebarExpanded ? expandedWidth : collapsedWidth

  return (
    <Box sx={{ display: 'flex', minHeight: '100vh' }}>
      <Sidebar expanded={sidebarExpanded} onToggle={() => setSidebarExpanded((expanded) => !expanded)} />
      <AppBar
        position="fixed"
        color="inherit"
        elevation={0}
        sx={{
          ml: `${drawerWidth}px`,
          width: `calc(100% - ${drawerWidth}px)`,
          borderBottom: 1,
          borderColor: 'divider',
          transition: (theme) => theme.transitions.create(['margin-left', 'width']),
        }}
      >
        <Toolbar sx={{ minHeight: 72, justifyContent: 'space-between' }}>
          <Box>
            <Typography variant="body2" sx={{ fontWeight: 700 }}>
              Local environment
            </Typography>
            <Typography variant="caption" color="text.secondary">
              Control plane workspace
            </Typography>
          </Box>
          <Stack direction="row" spacing={1} sx={{ alignItems: 'center' }}>
            <TextField
              select
              size="small"
              label="Auto update"
              value={refreshSpeed}
              onChange={(event) => onRefreshSpeedChange(event.target.value as RefreshSpeed)}
              sx={{ minWidth: 142 }}
              slotProps={{ select: { inputProps: { 'aria-label': 'Automatic update speed' } } }}
            >
              <MenuItem value="pause">⏸️ Paused</MenuItem>
              <MenuItem value="turtle">🐢 Turtle</MenuItem>
              <MenuItem value="llama">🦙 Llama</MenuItem>
              <MenuItem value="cheetah">🐆 Cheetah</MenuItem>
            </TextField>
            <Tooltip title={`Use ${mode === 'dark' ? 'light' : 'dark'} mode`}>
              <IconButton onClick={onToggleMode} aria-label={`Use ${mode === 'dark' ? 'light' : 'dark'} mode`}>
                {mode === 'dark' ? <Brightness7Rounded /> : <Brightness4Rounded />}
              </IconButton>
            </Tooltip>
          </Stack>
        </Toolbar>
      </AppBar>
      <Box
        component="main"
        sx={{
          flexGrow: 1,
          minWidth: 0,
          pt: '72px',
          transition: (theme) => theme.transitions.create('margin-left'),
        }}
      >
        <Box sx={{ p: { xs: 2.5, md: 4 }, maxWidth: 1280, mx: 'auto' }}>
          <HostWarnings />
          <Routes>
            <Route path="/" element={<Overview />} />
            <Route path="/activity" element={<PlaceholderPage title="Activity" description="Recent control plane events and operations." />} />
            <Route path="/compute/virtual-machines" element={<VirtualMachinesPage refreshInterval={refreshIntervals[refreshSpeed]} />} />
            <Route path="/compute/virtual-machines/new" element={<CreateVirtualMachinePage />} />
            <Route path="/compute/virtual-machines/:id/settings" element={<VirtualMachineSettingsPage />} />
            <Route
              path="/compute/virtual-machines/:id/console"
              element={
                <Suspense fallback={<Box sx={{ minHeight: 320, display: 'grid', placeItems: 'center' }}><CircularProgress /></Box>}>
                  <ConsolePage />
                </Suspense>
              }
            />
            <Route path="/compute/containers" element={<PlaceholderPage title="Containers" description="Manage Docker, Podman, and system containers." />} />
            <Route path="/configuration/storage" element={<StoragePage />} />
            <Route path="/configuration/cloud-init" element={<CloudInitPage />} />
            <Route path="/configuration/networking" element={<NetworkingPage refreshInterval={refreshIntervals[refreshSpeed]} />} />
            <Route path="/configuration/networking/bridges/:bridgeName" element={<BridgeSettingsPage refreshInterval={refreshIntervals[refreshSpeed]} />} />
            <Route path="/configuration/preferences" element={<PlaceholderPage title="Preferences" description="Tune the App Runner experience." />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </Box>
      </Box>
    </Box>
  )
}
