import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError, request, setApiMode, setCsrfToken, setUnauthorizedHandler } from './client'

describe('API client', () => {
  beforeEach(() => {
    document.cookie = 'merch_csrf=; Max-Age=0; Path=/'
    setCsrfToken('')
    setApiMode('normal')
    setUnauthorizedHandler()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('sends cookies and serializes JSON', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    vi.stubGlobal('fetch', fetchMock)

    await request('/articles', { method: 'POST', body: { name: 'Shirt' } })

    expect(fetchMock).toHaveBeenCalledOnce()
    const [url, options] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/v1/articles')
    expect(options.credentials).toBe('include')
    expect(options.body).toBe(JSON.stringify({ name: 'Shirt' }))
    expect(new Headers(options.headers).get('Content-Type')).toBe('application/json')
  })

  it('prefers the persisted CSRF cookie on unsafe requests', async () => {
    document.cookie = 'merch_csrf=cookie-token; Path=/'
    setCsrfToken('stale-memory-token')
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await request('/profile', { method: 'PATCH', body: {} })

    const options = fetchMock.mock.calls[0][1] as RequestInit
    expect(new Headers(options.headers).get('X-CSRF-Token')).toBe('cookie-token')
  })

  it('turns the stable server error shape into ApiError', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ code: 'feature_disabled', message: 'disabled' }), {
        status: 403,
        headers: { 'Content-Type': 'application/json' },
      }),
    ))

    await expect(request('/slideshow')).rejects.toMatchObject<ApiError>({
      status: 403,
      detailCode: 'feature_disabled',
      message: 'disabled',
    })
  })

  it('uses the separate sandbox prefix and CSRF cookie', async () => {
    document.cookie = 'merch_sandbox_csrf=sandbox-token; Path=/'
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await request('/sales', { method: 'POST', body: {}, apiMode: 'sandbox' })

    const [url, options] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/v1/sandbox/sales')
    expect(new Headers(options.headers).get('X-CSRF-Token')).toBe('sandbox-token')
  })

  it('advances the catalogue tutorial only after saving an article configuration', async () => {
    const progress = vi.fn()
    window.addEventListener('sandbox-progress', progress)
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ id: 1 }), {
        status: 201,
        headers: { 'Content-Type': 'application/json' },
      }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ id: 1 }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }))
    vi.stubGlobal('fetch', fetchMock)

    await request('/articles', { method: 'POST', body: { name: 'Hoodie' }, apiMode: 'sandbox' })
    expect(progress).not.toHaveBeenCalled()

    await request('/articles/1', { method: 'PUT', body: { option_groups: [] }, apiMode: 'sandbox' })
    expect(progress).toHaveBeenCalledOnce()

    window.removeEventListener('sandbox-progress', progress)
  })

  it('notifies the app shell when a request loses its session', async () => {
    const unauthorized = vi.fn()
    setUnauthorizedHandler(unauthorized)
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ message: 'expired' }), {
        status: 401,
        headers: { 'Content-Type': 'application/json' },
      }),
    ))

    await expect(request('/articles')).rejects.toMatchObject({ status: 401 })
    expect(unauthorized).toHaveBeenCalledWith('/articles')
  })
})
