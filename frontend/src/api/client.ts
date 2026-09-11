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
const SANDBOX_CSRF_COOKIE = 'merch_sandbox_csrf'

export type ApiMode = 'normal' | 'sandbox'
let apiMode: ApiMode = 'normal'

export function setApiMode(mode: ApiMode) {
  apiMode = mode
}

export function getApiMode() {
  return apiMode
}

export function apiUrl(path: string, mode: ApiMode = apiMode) {
  return `/api/v1${mode === 'sandbox' ? '/sandbox' : ''}${path}`
}

let csrfToken = ''
let unauthorizedHandler: ((path: string) => void) | undefined

export function setCsrfToken(token: string) {
  csrfToken = token
}

/** Installed by the app shell to reconcile an expired server session. */
export function setUnauthorizedHandler(handler?: (path: string) => void) {
  unauthorizedHandler = handler
}

/**
 * Reads the CSRF token, preferring the cookie.
 *
 * Keeping it only in memory meant a page reload silently broke every write:
 * the session cookie survived, the token did not. The cookie is readable on
 * purpose — a cross-origin attacker can neither read it nor set the header.
 */
function currentCsrfToken(mode: ApiMode): string {
  const cookieName = mode === 'sandbox' ? SANDBOX_CSRF_COOKIE : CSRF_COOKIE
  const match = document.cookie.match(new RegExp(`(?:^|; )${cookieName}=([^;]*)`))
  if (match) {
    return decodeURIComponent(match[1])
  }
  return csrfToken
}

export interface RequestOptions extends Omit<RequestInit, 'body'> {
  body?: unknown
  /** Set for multipart uploads; the body is then passed through untouched. */
  raw?: boolean
  apiMode?: ApiMode
}

export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const mode = options.apiMode ?? apiMode
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
    const token = currentCsrfToken(mode)
    if (token) {
      headers.set('X-CSRF-Token', token)
    }
  }

  const { apiMode: _apiMode, ...fetchOptions } = options
  const response = await fetch(apiUrl(path, mode), {
    ...fetchOptions,
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
    if (response.status === 401) unauthorizedHandler?.(path)
    const body =
      isJson && typeof payload === 'object' && payload !== null
        ? (payload as { message?: unknown; code?: unknown })
        : {}
    const message = String(body.message ?? response.statusText ?? 'Request failed')
    const code = typeof body.code === 'string' ? body.code : undefined
    throw new ApiError(response.status, message, code, payload)
  }

  if (mode === 'sandbox' && (
    (method === 'PUT' && path.startsWith('/articles/')) ||
    (method === 'POST' && (path === '/purchases' || path === '/sales')) ||
    (method === 'GET' && (path === '/balances' || path.startsWith('/exports/')))
  )) {
    window.dispatchEvent(new CustomEvent('sandbox-progress'))
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
