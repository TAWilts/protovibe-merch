/**
 * Thin fetch wrapper for the Go API.
 *
 * Authentication is a HttpOnly session cookie, so requests only need
 * `credentials: 'include'` plus the CSRF token for unsafe methods — the same
 * double-submit scheme the Flask original used via `X-CSRF-Token`.
 */

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
    /** The server's stable error code, which the UI maps to a translation. */
    readonly detailCode?: string,
    readonly detail?: unknown,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

const UNSAFE_METHODS = new Set(['POST', 'PUT', 'PATCH', 'DELETE'])

/** The cookie the server plants alongside the session. */
const CSRF_COOKIE = 'merch_csrf'

let csrfToken = ''

export function setCsrfToken(token: string) {
  csrfToken = token
}

/**
 * Reads the CSRF token, preferring the cookie.
 *
 * Keeping it only in memory meant a page reload silently broke every write:
 * the session cookie survived, the token did not. The cookie is readable on
 * purpose — a cross-origin attacker can neither read it nor set the header.
 */
function currentCsrfToken(): string {
  const match = document.cookie.match(new RegExp(`(?:^|; )${CSRF_COOKIE}=([^;]*)`))
  if (match) {
    return decodeURIComponent(match[1])
  }
  return csrfToken
}

export interface RequestOptions extends Omit<RequestInit, 'body'> {
  body?: unknown
  /** Set for multipart uploads; the body is then passed through untouched. */
  raw?: boolean
}

export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const method = (options.method ?? 'GET').toUpperCase()
  const headers = new Headers(options.headers)

  let body: BodyInit | undefined
  if (options.body !== undefined) {
    if (options.raw) {
      body = options.body as BodyInit
    } else {
      headers.set('Content-Type', 'application/json')
      body = JSON.stringify(options.body)
    }
  }
  if (UNSAFE_METHODS.has(method)) {
    const token = currentCsrfToken()
    if (token) {
      headers.set('X-CSRF-Token', token)
    }
  }

  const response = await fetch(`/api/v1${path}`, {
    ...options,
    method,
    headers,
    body,
    credentials: 'include',
  })

  if (response.status === 204) {
    return undefined as T
  }

  const isJson = response.headers.get('content-type')?.includes('application/json')
  const payload = isJson ? await response.json() : await response.text()

  if (!response.ok) {
    const body =
      isJson && typeof payload === 'object' && payload !== null
        ? (payload as { message?: unknown; code?: unknown })
        : {}
    const message = String(body.message ?? response.statusText ?? 'Request failed')
    const code = typeof body.code === 'string' ? body.code : undefined
    throw new ApiError(response.status, message, code, payload)
  }

  return payload as T
}

export const api = {
  get: <T>(path: string, options?: RequestOptions) => request<T>(path, { ...options, method: 'GET' }),
  post: <T>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>(path, { ...options, method: 'POST', body }),
  patch: <T>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>(path, { ...options, method: 'PATCH', body }),
  put: <T>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>(path, { ...options, method: 'PUT', body }),
  delete: <T>(path: string, options?: RequestOptions) =>
    request<T>(path, { ...options, method: 'DELETE' }),
}
