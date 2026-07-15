const configuredAPIBase = window.__VAULTMESH_CONFIG__?.apiBaseUrl || import.meta.env.VITE_API_BASE_URL
const developmentAPIBase = import.meta.env.DEV ? 'http://localhost:8080' : ''
const apiBaseURL = normalizeAPIBase(configuredAPIBase || developmentAPIBase)

export class APIError extends Error {
  constructor(
    message: string,
    public readonly code: string,
    public readonly status: number,
    public readonly retryAfterSeconds?: number,
    public readonly details?: unknown,
  ) {
    super(message)
    this.name = 'APIError'
  }
}

export interface JSONRequestOptions extends Omit<RequestInit, 'body'> {
  body?: unknown
}

export function getAPIBaseURL(): string {
  return apiBaseURL
}

export async function requestJSON<T>(path: string, options: JSONRequestOptions = {}): Promise<T> {
  const headers = new Headers(options.headers)
  headers.set('Accept', 'application/json')
  const body = options.body === undefined ? undefined : JSON.stringify(options.body)
  if (body !== undefined) headers.set('Content-Type', 'application/json')

  const response = await fetch(`${apiBaseURL}${path}`, { ...options, body, headers, credentials: 'include' })
  if (!response.ok) {
    let message = `请求失败（HTTP ${response.status}）`
    let code = 'http_error'
    let details: unknown
    try {
      const payload = await response.json() as { error?: { code?: string; message?: string; details?: unknown } }
      message = payload.error?.message ?? message
      code = payload.error?.code ?? code
      details = payload.error?.details
    } catch {
      // Use the bounded generic message above for non-JSON proxy errors.
    }
    const retryAfter = Number.parseInt(response.headers.get('Retry-After') || '', 10)
    throw new APIError(message, code, response.status, Number.isFinite(retryAfter) ? retryAfter : undefined, details)
  }
  if (response.status === 204) return undefined as T
  const contentType = response.headers.get('Content-Type') || ''
  if (!contentType.toLowerCase().startsWith('application/json')) {
    throw new APIError('API 返回了非 JSON 响应', 'invalid_response', response.status)
  }
  return response.json() as Promise<T>
}

function normalizeAPIBase(value: string): string {
  if (!value) throw new Error('VaultMesh Web 尚未配置 API 地址')
  if (value.length > 2048) throw new Error('VaultMesh API 地址过长')
  const parsed = new URL(value)
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    throw new Error('VaultMesh API 地址必须使用 HTTP 或 HTTPS')
  }
  if (parsed.username || parsed.password || parsed.search || parsed.hash) {
    throw new Error('VaultMesh API 地址不能包含凭据、查询参数或片段')
  }
  if (window.location.protocol === 'https:' && parsed.protocol !== 'https:') {
    throw new Error('HTTPS 控制台必须连接 HTTPS API')
  }
  return parsed.toString().replace(/\/+$/, '')
}
