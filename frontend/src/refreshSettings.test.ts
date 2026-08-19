import { describe, expect, it } from 'vitest'

import { defaultRefreshSpeed } from './refreshSettings'

describe('default refresh speed', () => {
  it.each(['localhost', 'LOCALHOST', '127.0.0.1', '127.42.9.3', '::1', '[::1]'])(
    'uses Cheetah for local host %s',
    (hostname) => expect(defaultRefreshSpeed(hostname)).toBe('cheetah'),
  )

  it.each(['10.0.0.1', '172.16.0.1', '172.31.255.254', '192.168.1.50'])(
    'uses Llama for RFC-1918 host %s',
    (hostname) => expect(defaultRefreshSpeed(hostname)).toBe('llama'),
  )

  it.each(['example.com', '8.8.8.8', '172.15.0.1', '172.32.0.1', '192.169.1.1'])(
    'uses Turtle for non-private host %s',
    (hostname) => expect(defaultRefreshSpeed(hostname)).toBe('turtle'),
  )
})
