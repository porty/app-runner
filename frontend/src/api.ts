const servicePath = '/twirp/apprunner.v1.AppRunnerService'

export interface PingResponse {
  message: string
  server_time: string
}

export interface EchoResponse {
  message: string
}

interface TwirpErrorBody {
  code?: string
  msg?: string
}

async function callTwirp<Response>(
  method: string,
  request: Record<string, unknown>,
): Promise<Response> {
  const response = await fetch(`${servicePath}/${method}`, {
    method: 'POST',
    headers: {
      Accept: 'application/json',
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(request),
  })

  if (!response.ok) {
    let error: TwirpErrorBody = {}
    try {
      error = (await response.json()) as TwirpErrorBody
    } catch {
      // The status text remains a useful fallback for non-Twirp intermediary errors.
    }

    throw new Error(error.msg ?? response.statusText ?? 'The RPC request failed')
  }

  return (await response.json()) as Response
}

export function ping(): Promise<PingResponse> {
  return callTwirp<PingResponse>('Ping', {})
}

export function echo(message: string): Promise<EchoResponse> {
  return callTwirp<EchoResponse>('Echo', { message })
}
