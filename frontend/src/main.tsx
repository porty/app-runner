import { StrictMode, useMemo, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { CssBaseline, ThemeProvider, createTheme } from '@mui/material'

import App from './App'
import './index.css'
import {
  defaultRefreshSpeed,
  isRefreshSpeed,
  refreshSpeedStorageKey,
  type RefreshSpeed,
} from './refreshSettings'

type ColourMode = 'light' | 'dark'

function initialColourMode(): ColourMode {
  const savedMode = localStorage.getItem('app-runner-colour-mode')
  if (savedMode === 'light' || savedMode === 'dark') {
    return savedMode
  }
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

function initialRefreshSpeed(): RefreshSpeed {
  const savedSpeed = localStorage.getItem(refreshSpeedStorageKey)
  return isRefreshSpeed(savedSpeed) ? savedSpeed : defaultRefreshSpeed(window.location.hostname)
}

function Root() {
  const [mode, setMode] = useState<ColourMode>(initialColourMode)
  const [refreshSpeed, setRefreshSpeed] = useState<RefreshSpeed>(initialRefreshSpeed)
  const theme = useMemo(
    () =>
      createTheme({
        palette: {
          mode,
          primary: { main: mode === 'dark' ? '#7dd3fc' : '#0369a1' },
          secondary: { main: mode === 'dark' ? '#c4b5fd' : '#7c3aed' },
          background: {
            default: mode === 'dark' ? '#0b1120' : '#f5f7fb',
            paper: mode === 'dark' ? '#111827' : '#ffffff',
          },
        },
        shape: { borderRadius: 12 },
        typography: {
          fontFamily:
            'Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif',
          h4: { fontWeight: 750, letterSpacing: '-0.03em' },
          h6: { fontWeight: 700 },
          button: { textTransform: 'none', fontWeight: 650 },
        },
        components: {
          MuiCard: {
            styleOverrides: {
              root: { backgroundImage: 'none' },
            },
          },
        },
      }),
    [mode],
  )

  const toggleMode = () => {
    setMode((currentMode) => {
      const nextMode = currentMode === 'dark' ? 'light' : 'dark'
      localStorage.setItem('app-runner-colour-mode', nextMode)
      return nextMode
    })
  }

  const changeRefreshSpeed = (speed: RefreshSpeed) => {
    localStorage.setItem(refreshSpeedStorageKey, speed)
    setRefreshSpeed(speed)
  }

  return (
    <ThemeProvider theme={theme}>
      <CssBaseline />
      <BrowserRouter>
        <App
          mode={mode}
          onToggleMode={toggleMode}
          refreshSpeed={refreshSpeed}
          onRefreshSpeedChange={changeRefreshSpeed}
        />
      </BrowserRouter>
    </ThemeProvider>
  )
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <Root />
  </StrictMode>,
)
