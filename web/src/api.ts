const tokenKey = 'vaultmesh.adminToken'

export class APIError extends Error {
  constructor(
    message: string,
    public readonly code: string,
    public readonly status: number,
  ) {
    super(message)
  }
}

export function getToken(): string {
  return sessionStorage.getItem(tokenKey) ?? ''
}

export function setToken(token: string): void {
  if (token) {
    sessionStorage.setItem(tokenKey, token)
  } else {
    sessionStorage.removeItem(tokenKey)
  }
}

export async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers)
  headers.set('Accept', 'application/json')
  const token = getToken()
  if (token) headers.set('Authorization', `Bearer ${token}`)
  if (options.body) headers.set('Content-Type', 'application/json')

  const response = await fetch(path, { ...options, headers })
  if (!response.ok) {
    let message = `请求失败（HTTP ${response.status}）`
    let code = 'http_error'
    try {
      const payload = await response.json() as { error?: { code?: string; message?: string } }
      message = payload.error?.message ?? message
      code = payload.error?.code ?? code
    } catch {
      // Use the bounded generic message above for non-JSON proxy errors.
    }
    throw new APIError(message, code, response.status)
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}
