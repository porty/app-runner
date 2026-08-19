import { useEffect, useState } from 'react'
import { Alert, AlertTitle } from '@mui/material'

import { getHostStatus, type HostStatus } from './api'

const dismissedWarningKey = 'app-runner-dismissed-bridge-warning'

export default function HostWarnings() {
  const [status, setStatus] = useState<HostStatus | null>(null)
  const [dismissed, setDismissed] = useState(() => localStorage.getItem(dismissedWarningKey) ?? '')

  useEffect(() => {
    void getHostStatus().then(setStatus).catch(() => undefined)
  }, [])

  if (!status || status.bridge_available || !status.bridge_warning || dismissed === status.bridge_warning) {
    return null
  }

  return (
    <Alert
      severity="warning"
      onClose={() => {
        localStorage.setItem(dismissedWarningKey, status.bridge_warning)
        setDismissed(status.bridge_warning)
      }}
      sx={{ mb: 3 }}
    >
      <AlertTitle>Bridge networking is unavailable</AlertTitle>
      {status.bridge_warning}
    </Alert>
  )
}

