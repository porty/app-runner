export type RefreshSpeed = 'pause' | 'turtle' | 'llama' | 'cheetah'

export const refreshSpeedStorageKey = 'app-runner-refresh-speed'

export const refreshIntervals: Record<RefreshSpeed, number | null> = {
  pause: null,
  turtle: 10_000,
  llama: 5_000,
  cheetah: 1_000,
}

export function isRefreshSpeed(value: string | null): value is RefreshSpeed {
  return value === 'pause' || value === 'turtle' || value === 'llama' || value === 'cheetah'
}

export function defaultRefreshSpeed(hostname: string): RefreshSpeed {
  const host = hostname.toLowerCase().replace(/^\[|\]$/g, '')
  const octets = host.split('.').map(Number)
  const isIPv4 =
    octets.length === 4 &&
    octets.every((octet) => Number.isInteger(octet) && octet >= 0 && octet <= 255)

  if (host === 'localhost' || host === '::1' || (isIPv4 && octets[0] === 127)) {
    return 'cheetah'
  }

  if (
    isIPv4 &&
    (octets[0] === 10 ||
      (octets[0] === 172 && octets[1] >= 16 && octets[1] <= 31) ||
      (octets[0] === 192 && octets[1] === 168))
  ) {
    return 'llama'
  }

  return 'turtle'
}
