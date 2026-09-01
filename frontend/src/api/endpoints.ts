import { api } from './client'
import type {
  Article,
  AuditEntry,
  BackupRun,
  Band,
  BandSummary,
  BandUser,
  BalancesPayload,
  BandLedger,
  BandTransaction,
  DeliveryStatus,
  Identity,
  LoginResponse,
  Attachment,
  ImportPreview,
  ImportResult,
  MfaEnrollmentStart,
  UpdateStatus,
  PaymentQRAvailability,
  PaymentQRIntent,
  PaymentQRSettings,
  Photo,
  PlatformSettings,
  PlatformUser,
  ProfilePayload,
  Purchase,
  Queues,
  Role,
  Receipt,
  SaleEvent,
  SaleResult,
  SupportGrant,
  SupportMessage,
} from './types'

/** Authentication and identity. */
export const authApi = {
  login: (band: string, username: string, secret: string) =>
    api.post<LoginResponse>('/auth/login', { band, username, secret }),
  completeMfa: (pendingToken: string, code: string) =>
    api.post<LoginResponse>('/auth/mfa', { pending_token: pendingToken, code }),
  completePasswordSetup: (pendingToken: string, password: string) =>
    api.post<LoginResponse>('/auth/password-setup', {
      pending_token: pendingToken,
      password,
    }),
  requestPasswordReset: (username: string) =>
    api.post<{ message: string }>('/auth/password-reset/request', { username }),
  confirmPasswordReset: (username: string, code: string, newPassword: string) =>
    api.post<void>('/auth/password-reset/confirm', {
      username, code, new_password: newPassword,
    }),
  startEnrollment: (pendingToken?: string) =>
    api.post<MfaEnrollmentStart>('/mfa/enrollment/start', {
      pending_token: pendingToken ?? '',
    }),
  confirmEnrollment: (code: string, pendingToken?: string) =>
    api.post<{ recovery_codes: string[] } & LoginResponse>('/mfa/enrollment/confirm', {
      pending_token: pendingToken ?? '',
      code,
    }),
  logout: () => api.post<void>('/auth/logout'),
  me: () => api.get<Identity>('/me'),
  setPosMode: (enabled: boolean) =>
    api.post<{ pos_mode: boolean }>('/session/pos-mode', { enabled }),
}

/** Catalogue: the full view for management, the offered subset for selling. */
export const catalogueApi = {
  list: () => api.get<{ articles: Article[] }>('/articles'),
  assortment: () =>
    api.get<{ articles: Article[]; payment_methods: string[] }>('/assortment'),
  create: (payload: {
    name: string
    default_sale_price_cents: number
    default_purchase_price_cents: number
  }) => api.post<Article>('/articles', payload),
  save: (id: number, payload: unknown) => api.put<Article>(`/articles/${id}`, payload),
}

export interface BookSalePayload {
  items: { variant_id: number; quantity: number; unit_price_cents?: number }[]
  payment_method: string
  is_paid: boolean
  is_received: boolean
  amount_given_cents?: number | null
  customer_name?: string
  customer_address?: string
  event_name?: string
  sold_by?: string
  comment?: string
  sold_on?: string
  receipt_id?: string
  /** Redeems a displayed payment code; the reservation becomes the receipt. */
  payment_qr_intent_token?: string
  /** Set when replaying a sale queued while the device was offline. */
  client_event_id?: string
  client_device_id?: string
  client_created_at?: string
}

export const salesApi = {
  /**
   * Proposes the next receipt ID. It is explicitly provisional: a concurrent
   * sale may take the number first, which is why the booking settles it.
   */
  receiptPreview: (kind: 'sale' | 'purchase' = 'sale', date?: string) => {
    const query = new URLSearchParams({ kind })
    if (date) query.set('date', date)
    return api.get<{ receipt_id: string; provisional: boolean }>(`/receipt-preview?${query}`)
  },
  book: (payload: BookSalePayload) => api.post<SaleResult>('/sales', payload),
  /** Which codes the band can show at all. */
  paymentQrAvailability: () =>
    api.get<PaymentQRAvailability>('/payment-qr/availability'),
  /**
   * Reserves a receipt number and renders the code. The server prices the
   * basket itself, so a tampered client cannot show the customer a wrong total.
   */
  createPaymentQrIntent: (payload: {
    method: string
    sale: BookSalePayload
    description?: string
  }) => api.post<PaymentQRIntent>('/payment-qr/intents', payload),
  /** Releases the reservation when the customer walks away mid-scan. */
  cancelPaymentQrIntent: (token: string) =>
    api.post<void>(`/payment-qr/intents/${token}/cancel`),
  events: () =>
    api.get<{ events: SaleEvent[]; selected_event_id: number }>('/sale-events'),
  createEvent: (name: string, select = true) =>
    api.post<SaleEvent>('/sale-events', { name, select }),
  selectEvent: (id: number) => api.post<SaleEvent>(`/sale-events/${id}/select`),
}

export const operationsApi = {
  history: (limit?: number) =>
    api.get<{ receipts: Receipt[] }>(`/history${limit ? `?limit=${limit}` : ''}`),
  queues: () => api.get<Queues>('/operations'),
  cancel: (saleId: number, scope: 'item' | 'receipt') =>
    api.patch<{ cancelled_ids: number[] }>(`/sales/${saleId}/cancel`, { scope }),
  setDeliveryStatus: (saleId: number, status: DeliveryStatus) =>
    api.patch<void>(`/sales/${saleId}/delivery-status`, { status }),
  markPaid: (saleId: number) => api.patch<void>(`/sales/${saleId}/payment-status`),
}

export const reportsApi = {
  balances: () => api.get<BalancesPayload>('/balances'),
  bandLedger: () => api.get<BandLedger>('/band-finances'),
  createBandEntry: (payload: {
    transaction_type: 'income' | 'expense'
    transaction_on: string
    category: string
    description: string
    amount_cents: number
  }) => api.post<BandTransaction>('/band-finances', payload),
  cancelBandEntry: (id: number) => api.post<void>(`/band-finances/${id}/cancel`),
}

export const purchasesApi = {
  list: () => api.get<{ purchases: Purchase[] }>('/purchases'),
  create: (payload: {
    items: { variant_id: number; quantity: number; unit_cost_cents: number; comment?: string }[]
    purchased_on: string
    supplier?: string
    invoice_reference?: string
    receipt_id?: string
  }) => api.post<{ receipt_id: string; purchase_ids: number[]; total_cost_cents: number }>('/purchases', payload),
  update: (id: number, payload: { quantity: number; unit_cost_cents: number; comment?: string }) =>
    api.patch<void>(`/purchases/${id}`, payload),
  remove: (id: number) => api.delete<void>(`/purchases/${id}`),
  lastCost: (variantId: number) =>
    api.get<{ unit_cost_cents: number; found: boolean }>(`/purchases/last-cost/${variantId}`),
}

/** Exports are plain links so the browser handles the download itself. */
export const exportUrls = {
  csv: (kind: 'artikel' | 'verkaeufe' | 'einkaeufe' | 'bestand') => `/api/v1/exports/${kind}.csv`,
  zip: () => '/api/v1/exports/all.zip',
}

/** The control plane. Every call here requires a platform account. */
export const platformApi = {
  bands: (includeDeleted = false) =>
    api.get<{ bands: BandSummary[] }>(`/platform/bands${includeDeleted ? '?include_deleted=true' : ''}`),
  createBand: (payload: { slug: string; name: string; contact_email?: string }) =>
    api.post<Band>('/platform/bands', payload),
  updateBand: (id: number, payload: Record<string, unknown>) =>
    api.patch<Band>(`/platform/bands/${id}`, payload),
  activateBand: (id: number) => api.post<void>(`/platform/bands/${id}/activate`),
  deactivateBand: (id: number) => api.post<void>(`/platform/bands/${id}/deactivate`),
  deleteBand: (id: number) => api.delete<void>(`/platform/bands/${id}`),
  restoreBand: (id: number) => api.post<void>(`/platform/bands/${id}/restore`),
  revokeBandSessions: (id: number) => api.post<void>(`/platform/bands/${id}/revoke-sessions`),
  /**
   * Hands a band its first administrator. The role is fixed server-side, so
   * there is nothing to pass but the name.
   */
  createBandAdmin: (id: number, username: string) =>
    api.post<{ id: number; username: string; role: Role; setup_code: string }>(
      `/platform/bands/${id}/admins`,
      { username },
    ),

  users: () =>
    api.get<{ users: PlatformUser[]; assignable_roles: Role[] }>('/platform/users'),
  createUser: (username: string, contactEmail: string, role: Role) =>
    api.post<{ id: number; username: string; role: Role; setup_code: string }>(
      '/platform/users', { username, contact_email: contactEmail, role },
    ),
  resetUserPassword: (id: number) =>
    api.post<{ username: string; setup_code: string }>(`/platform/users/${id}/reset-password`),
  changeUserRole: (id: number, role: Role) =>
    api.patch<void>(`/platform/users/${id}/role`, { role }),
  setUserActive: (id: number, active: boolean) =>
    api.patch<void>(`/platform/users/${id}/active`, { active }),
  resetUserMfa: (id: number) => api.post<void>(`/platform/users/${id}/reset-mfa`),

  grants: (bandId?: number) =>
    api.get<{ grants: SupportGrant[] }>(
      `/platform/support-access${bandId ? `?band_id=${bandId}` : ''}`,
    ),
  requestAccess: (payload: {
    band_id: number
    reason: string
    scope: 'read_only' | 'read_write'
    duration_seconds: number
  }) => api.post<SupportGrant>('/platform/support-access', payload),
  activateAccess: (id: number, code: string) =>
    api.post<SupportGrant>(`/platform/support-access/${id}/activate`, { code }),
  revokeAccess: (id: number) => api.post<void>(`/platform/support-access/${id}/revoke`),

  audit: (params: Record<string, string>) =>
    api.get<{ entries: AuditEntry[]; limit: number }>(
      `/platform/audit?${new URLSearchParams(params)}`,
    ),
  settings: () => api.get<PlatformSettings>('/platform/settings'),
  /** Advisory only — it reports a newer release, it never installs one. */
  updates: (force = false) =>
    api.get<UpdateStatus>(`/platform/updates${force ? '?force=true' : ''}`),
  sendTestMail: (to: string) =>
    api.post<{ to: string }>('/platform/settings/test-mail', { to }),
  saveSettings: (payload: Record<string, unknown>) =>
    api.put<PlatformSettings>('/platform/settings', payload),

  backups: (bandId?: number) =>
    api.get<{ runs: BackupRun[] }>(`/platform/backups${bandId ? `?band_id=${bandId}` : ''}`),
  runBackup: (bandId?: number) =>
    api.post<BackupRun>('/platform/backups', { band_id: bandId ?? null }),
  /** Puts one band back to a captured state; returns the safety point taken. */
  restoreBackup: (id: number) =>
    api.post<{ safety_run: BackupRun }>(`/platform/backups/${id}/restore`),
  /** Drops runs past the retention window. */
  pruneBackups: () => api.post<{ removed: number }>('/platform/backups/prune'),

  messages: (openOnly = false) =>
    api.get<{ messages: SupportMessage[] }>(`/platform/messages${openOnly ? '?open=true' : ''}`),
  resolveMessage: (id: number, resolved: boolean) =>
    api.post<void>(`/platform/messages/${id}/resolve`, { resolved }),
}

/** The band's own side of the support-access workflow. */
export const bandAdminApi = {
  grants: () => api.get<{ grants: SupportGrant[] }>('/band-admin/support-access'),
  approve: (id: number, note?: string) =>
    api.post<SupportGrant>(`/band-admin/support-access/${id}/approve`, { note: note ?? '' }),
  deny: (id: number, note?: string) =>
    api.post<SupportGrant>(`/band-admin/support-access/${id}/deny`, { note: note ?? '' }),
  revoke: (id: number) => api.post<void>(`/band-admin/support-access/${id}/revoke`),
  reauth: (password: string, code?: string) =>
    api.post<{ valid_for_seconds: number }>('/profile/reauth', { password, code: code ?? '' }),
}

export const supportApi = {
  send: (payload: {
    message_type: 'issue' | 'question'
    subject: string
    body: string
    sender_email?: string
  }) => api.post<{ id: number }>('/support-messages', payload),
  mine: () => api.get<{ messages: SupportMessage[] }>('/support-messages'),
  announcement: () =>
    api.get<{ announcement: { text: string; level: string } | null }>('/announcement'),
}

/**
 * Invoices and receipt attachments. Uploads go out as multipart, so the body
 * is passed through untouched rather than serialised as JSON.
 */
/**
 * CSV import. The preview is always run first: an import creates articles and
 * variants as a side effect, so the band sees what it is about to gain before
 * anything is written.
 */
export const importApi = {
  preview: (kind: ImportKind, file: File) => {
    const body = new FormData()
    body.append('file', file)
    return api.post<ImportPreview>(`/imports/${kind}/preview`, body, { raw: true })
  },
  apply: (kind: ImportKind, file: File) => {
    const body = new FormData()
    body.append('file', file)
    return api.post<ImportResult>(`/imports/${kind}/apply`, body, { raw: true })
  },
}

export type ImportKind = 'einkaeufe' | 'verkaeufe'

export const attachmentsApi = {
  uploadInvoice: (purchaseId: number, file: File) => {
    const body = new FormData()
    body.append('file', file)
    return api.post<{ original_filename: string; size_bytes: number }>(
      `/purchases/${purchaseId}/invoice`,
      body,
      { raw: true },
    )
  },
  removeInvoice: (purchaseId: number) => api.delete<void>(`/purchases/${purchaseId}/invoice`),
  invoiceUrl: (purchaseId: number) => `/api/v1/purchases/${purchaseId}/invoice`,

  list: (receiptId: string) =>
    api.get<{ attachments: Attachment[] }>(
      `/purchase-receipts/${encodeURIComponent(receiptId)}/attachments`,
    ),
  upload: (receiptId: string, file: File) => {
    const body = new FormData()
    body.append('file', file)
    return api.post<Attachment>(
      `/purchase-receipts/${encodeURIComponent(receiptId)}/attachments`,
      body,
      { raw: true },
    )
  },
  remove: (receiptId: string, attachmentId: number) =>
    api.delete<void>(
      `/purchase-receipts/${encodeURIComponent(receiptId)}/attachments/${attachmentId}`,
    ),
  fileUrl: (receiptId: string, attachmentId: number) =>
    `/api/v1/purchase-receipts/${encodeURIComponent(receiptId)}/attachments/${attachmentId}`,
}

export const photosApi = {
  list: () => api.get<{ photos: Photo[] }>('/photos'),
  /**
   * Uploads a picture. Without a variant it is a slideshow extra; with one it
   * is that variant's product photo and shows up at the point of sale.
   */
  upload: (file: File, variantId?: number) => {
    const body = new FormData()
    body.append('file', file)
    if (variantId) body.append('variant_id', String(variantId))
    return api.post<Photo>('/photos', body, { raw: true })
  },
  slideshow: () =>
    api.get<{ photos: Photo[]; collage_show_prices: boolean }>('/slideshow'),
  update: (id: number, payload: { include_in_slideshow?: boolean; show_price?: boolean }) =>
    api.patch<void>(`/photos/${id}`, payload),
  remove: (id: number) => api.delete<void>(`/photos/${id}`),
  setCollagePrices: (value: boolean) =>
    api.patch<void>('/slideshow/settings', { collage_show_prices: value }),
  fileUrl: (id: number) => `/api/v1/photos/${id}/file`,
}

export const bandUsersApi = {
  list: () =>
    api.get<{ users: BandUser[]; assignable_roles: Role[] }>('/band-admin/users'),
  create: (username: string, role: Role) =>
    api.post<{ id: number; username: string; role: Role; setup_code: string }>(
      '/band-admin/users',
      { username, role },
    ),
  resetPassword: (id: number) =>
    api.post<{ username: string; setup_code: string }>(`/band-admin/users/${id}/reset-password`),
  changeRole: (id: number, role: Role) =>
    api.patch<void>(`/band-admin/users/${id}/role`, { role }),
  setActive: (id: number, active: boolean) =>
    api.patch<void>(`/band-admin/users/${id}/active`, { active }),
  resetMfa: (id: number) => api.post<void>(`/band-admin/users/${id}/reset-mfa`),
  remove: (id: number) => api.delete<void>(`/band-admin/users/${id}`),
  /** Where the band's payment codes send the money. */
  paymentQrSettings: () => api.get<PaymentQRSettings>('/payment-qr/settings'),
  savePaymentQrSettings: (payload: PaymentQRSettings) =>
    api.put<PaymentQRSettings>('/payment-qr/settings', payload),
}

export const profileApi = {
  get: () => api.get<ProfilePayload>('/profile'),
  reauth: (password: string, code?: string) =>
    api.post<{ valid_for_seconds: number }>('/profile/reauth', { password, code: code ?? '' }),
  personalization: (payload: {
    ui_theme?: string
    ui_language?: string
    show_variant_photos?: boolean
  }) => api.patch<void>('/profile/personalization', payload),
  changePassword: (currentPassword: string, newPassword: string) =>
    api.post<{ signed_out: boolean }>('/profile/password', {
      current_password: currentPassword,
      new_password: newPassword,
    }),
  changeUsername: (username: string) =>
    api.post<{ username: string }>('/profile/username', { username }),
  changeContactEmail: (contactEmail: string) =>
    api.put<{ contact_email: string }>('/profile/contact-email', { contact_email: contactEmail }),
  startMfa: () => api.post<MfaEnrollmentStart>('/mfa/enrollment/start', {}),
  confirmMfa: (code: string) =>
    api.post<{ recovery_codes: string[] }>('/mfa/enrollment/confirm', { code }),
  disableMfa: (code: string) => api.post<void>('/profile/mfa/disable', { code }),
  regenerateRecoveryCodes: () =>
    api.post<{ recovery_codes: string[] }>('/profile/mfa/recovery-codes'),
}
