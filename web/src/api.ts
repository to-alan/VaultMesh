const tokenKey = 'vaultmesh.adminToken'
const configuredAPIBase = window.__VAULTMESH_CONFIG__?.apiBaseUrl || import.meta.env.VITE_API_BASE_URL
const developmentAPIBase = import.meta.env.DEV ? 'http://127.0.0.1:8080' : ''
const apiBaseURL = normalizeAPIBase(configuredAPIBase || developmentAPIBase)

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

export function getAPIBaseURL(): string {
  return apiBaseURL
}

export async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers)
  headers.set('Accept', 'application/json')
  const token = getToken()
  if (token) headers.set('Authorization', `Bearer ${token}`)
  if (options.body) headers.set('Content-Type', 'application/json')

  const response = await fetch(`${apiBaseURL}${path}`, { ...options, headers })
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

function normalizeAPIBase(value: string): string {
  if (!value) throw new Error('VaultMesh Web 尚未配置 API 地址')
  const parsed = new URL(value)
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    throw new Error('VaultMesh API 地址必须使用 HTTP 或 HTTPS')
  }
  return parsed.toString().replace(/\/+$/, '')
}
