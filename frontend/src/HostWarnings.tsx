import { useEffect, useState } from 'react'
import { Alert, AlertTitle, Button } from '@mui/material'
import { Link } from 'react-router-dom'

import { getNetworkingStatus, type NetworkingStatus } from './api'

const dismissedWarningKey = 'app-runner-dismissed-bridge-warning'

export default function HostWarnings() {
  const [status, setStatus] = useState<NetworkingStatus | null>(null)
  const [dismissed, setDismissed] = useState(() => localStorage.getItem(dismissedWarningKey) ?? '')

  useEffect(() => {
    void getNetworkingStatus().then(setStatus).catch(() => undefined)
  }, [])

  const bridges = status?.bridges ?? []
  const warning = bridges.length === 0
    ? 'No Linux bridges are available for bridged workloads.'
    : bridges.some((bridge) => bridge.usable_by_qemu)
      ? ''
      : `${bridges.length} bridge${bridges.length === 1 ? '' : 's'} detected, but none currently pass the QEMU access diagnostics.`

  if (!status || !warning || dismissed === warning) {
    return null
  }

  return (
    <Alert
      severity="warning"
      onClose={() => {
        localStorage.setItem(dismissedWarningKey, warning)
        setDismissed(warning)
      }}
      action={<Button component={Link} to="/configuration/networking" color="inherit" size="small">Diagnostics</Button>}
      sx={{ mb: 3 }}
    >
      <AlertTitle>Bridge networking is unavailable</AlertTitle>
      {warning}
    </Alert>
  )
}
