/** Shared types for the API payloads. */

export type Role =
  | 'seller'
  | 'member'
  | 'manager'
  | 'band_admin'
  | 'support_admin'
  | 'system_admin'

export interface Capabilities {
  role: Role
  role_label: string
  is_band_admin: boolean
  is_support_admin: boolean
  is_system_admin: boolean
  is_platform_staff: boolean
  can_access_band_workflows: boolean
  can_access_member_workflows: boolean
  can_manage_purchases: boolean
  can_manage_band_finances: boolean
  can_manage_articles: boolean
  can_manage_slideshow: boolean
  can_access_band_administration: boolean
  can_access_system_administration: boolean
  can_manage_platform_staff: boolean
  can_manage_updates: boolean
  mfa_required: boolean
  mfa_enabled: boolean
  sensitive_action_mfa_required: boolean
}

export interface CurrentUser {
  id: number
  username: string
  role: Role
  ui_theme: string
  ui_language: string
  show_variant_photos: boolean
  mfa_enabled: boolean
  contact_email: string
}

export interface IdentityBandSummary {
  id: number
  slug: string
  name: string
  feature_flags: Required<FeatureFlags>
  maintenance_message?: string
}

/** Shown as a persistent banner to both sides while support access is live. */
export interface SupportGrantBanner {
  id: number
  scope: 'read_only' | 'read_write'
  reason: string
  expires_at: string
  username: string
}

export interface Identity {
  user: CurrentUser
  band?: IdentityBandSummary
  capabilities: Capabilities
  pos_mode: boolean
  support_grant?: SupportGrantBanner
}

export interface LoginResponse {
  needs_password_setup: boolean
  needs_mfa: boolean
  needs_mfa_enrollment: boolean
  pending_token?: string
  session?: Identity
  csrf_token?: string
}

/** What a band has configured, so the sales page only offers usable codes. */
export interface PaymentQRAvailability {
  paypal: boolean
  bank: boolean
}

export interface PaymentQRSettings {
  paypal_me_url: string
  bank_account_holder: string
  bank_iban: string
  bank_bic: string
  bank_remittance_text: string
}

/** A reserved receipt number with its rendered code. Nothing is booked yet. */
export interface PaymentQRIntent {
  token: string
  receipt_id: string
  method: string
  amount_cents: number
  image_data_uri: string
  payload_hint: string
  expires_at: string
}

/** The advisory release check; the instance never updates itself. */
export interface UpdateStatus {
  current: string
  latest: string
  newer_available: boolean
  url: string
  notes: string
  checked_at: string
  cached_at?: string
}

/** What a CSV import would change, reported before anything is written. */
export interface ImportPreview {
  kind: 'einkaeufe' | 'verkaeufe'
  row_count: number
  new_articles: string[]
  new_option_values: string[]
  new_variants: number
  total_quantity: number
  total_cents: number
}

export interface ImportResult {
  receipt_id: string
  row_count: number
  total_cents: number
}

/** One file hanging off a goods-receipt number. */
export interface Attachment {
  id: number
  original_filename: string
  size_bytes: number
}

export interface MfaEnrollmentStart {
  secret: string
  otpauth_uri: string
  /** The provisioning URI as a PNG data URI, rendered by the backend. */
  otpauth_qr: string
}

export interface OptionValue {
  id: number
  value: string
  position: number
  is_active: boolean
}

export interface OptionGroup {
  id: number
  name: string
  position: number
  is_active: boolean
  values: OptionValue[]
}

export interface Variant {
  id: number
  option_value_ids: number[]
  combination_key: string
  sale_price_cents: number
  default_purchase_price_cents: number
  minimum_stock: number | null
  is_offered: boolean
  is_available_for_sale: boolean
  no_reorder: boolean
  is_active: boolean
  purchased: number
  sold: number
  on_hand: number
  below_minimum: boolean
  /** Picture ids in display order; fetch each via photosApi.fileUrl. */
  photo_ids: number[]
}

export interface Article {
  id: number
  name: string
  default_sale_price_cents: number
  default_purchase_price_cents: number
  is_offered: boolean
  is_active: boolean
  configuration_complete: boolean
  total_stock: number
  option_groups: OptionGroup[]
  variants: Variant[]
}

export interface SaleEvent {
  id: number
  name: string
  is_selected: boolean
}

/** One position a seller added to the basket, before it is booked. */
export interface BasketLine {
  variantId: number
  articleId: number
  label: string
  quantity: number
  unitPriceCents: number
  onHand: number
}

export interface SaleResult {
  receipt_id: string
  sale_ids: number[]
  total_due_cents: number
  donation_cents: number
  replayed: boolean
}

export type DeliveryStatus = 'not_applicable' | 'pending' | 'shipped' | 'received'

export interface Position {
  id: number
  /** What identifies the case this line belongs to. */
  receipt_id: string
  sold_on: string
  payment_method: string
  customer_name: string
  customer_address: string
  event_name: string
  comment: string
  variant_id: number
  article_name: string
  variant_label: string
  quantity: number
  unit_price_cents: number
  amount_due_cents: number
  amount_given_cents: number | null
  donation_cents: number
  is_paid: boolean
  payment_follow_up: boolean
  is_received: boolean
  delivery_status: DeliveryStatus
  is_cancelled: boolean
}

export interface Receipt {
  receipt_id: string
  sold_on: string
  payment_method: string
  customer_name: string
  customer_address: string
  event_name: string
  sold_by: string
  comment: string
  total_due_cents: number
  total_given_cents: number
  donation_cents: number
  is_fully_cancelled: boolean
  positions: Position[]
}

export interface Queues {
  open_shipments: Position[]
  delivered_shipments: Position[]
  open_payments: Position[]
  settled_payments: Position[]
}

export interface BalanceRow {
  variant_id: number
  article_id: number
  article_name: string
  variant_label: string
  purchased: number
  sold: number
  on_hand: number
  minimum_stock: number | null
  below_minimum: boolean
  purchase_cost_cents: number
  revenue_cents: number
  collected_cents: number
  donation_cents: number
  sale_price_cents: number
  default_purchase_price_cents: number
  is_offered: boolean
  is_available_for_sale: boolean
  no_reorder: boolean
  is_active: boolean
}

export interface BalanceSummary {
  purchase_cost_cents: number
  revenue_cents: number
  collected_cents: number
  donation_cents: number
  cash_balance_cents: number
  outstanding_cents: number
  pending_delivery_count: number
  stock_count: number
  minimum_stock_warning_count: number
  band_income_cents: number
  band_expense_cents: number
  band_balance_cents: number
  overall_balance_cents: number
}

export interface RankingEntry {
  label: string
  quantity: number
  income_cents: number
  profit_cents: number
}

export interface DailyIncome {
  date: string
  income_cents: number
  sale_count: number
}

export interface BalancesPayload {
  summary: BalanceSummary
  reorder_rows: BalanceRow[]
  obsolete_rows: BalanceRow[]
  top_selling_items: RankingEntry[]
  top_revenue_items: RankingEntry[]
  top_events: RankingEntry[]
  top_sellers: RankingEntry[]
  daily_income: DailyIncome[]
}

export interface Purchase {
  id: number
  receipt_id: string
  variant_id: number
  article_name: string
  variant_label: string
  quantity: number
  unit_cost_cents: number
  total_cost_cents: number
  purchased_on: string
  supplier: string
  invoice_reference: string
  has_invoice_file: boolean
  comment: string
  created_by_username: string
}

export interface BandTransaction {
  id: number
  transaction_type: 'income' | 'expense'
  transaction_on: string
  category: string
  description: string
  amount_cents: number
  is_cancelled: boolean
  created_by_username: string
}

export interface CategoryTotal {
  category: string
  income_cents: number
  expense_cents: number
  balance_cents: number
}

export interface BandLedger {
  entries: BandTransaction[]
  categories: CategoryTotal[]
  suggested_categories: string[]
  income_cents: number
  expense_cents: number
  balance_cents: number
}

export interface FeatureFlags {
  slideshow?: boolean
  band_finances?: boolean
  payment_qr?: boolean
  offline_sales?: boolean
  csv_import?: boolean
}

export interface Band {
  id: number
  slug: string
  name: string
  contact_email: string
  is_active: boolean
  deactivated_at: string | null
  deleted_at: string | null
  maintenance_message: string
  storage_quota_bytes: number
  user_quota: number
  feature_flags: FeatureFlags
  created_at: string
  updated_at: string
}

export interface BandSummary extends Band {
  user_count: number
  article_count: number
  sale_count: number
  storage_bytes: number
  last_activity_at: string | null
  last_backup_at: string | null
  active_grant_id: number | null
}

export type GrantStatus =
  | 'pending'
  | 'approved'
  | 'denied'
  | 'active'
  | 'expired'
  | 'revoked'

export interface SupportGrant {
  id: number
  band_id: number
  requested_by_user_id: number
  requested_by_username: string
  reason: string
  scope: 'read_only' | 'read_write'
  requested_duration_seconds: number
  status: GrantStatus
  decided_by_username: string
  decided_at: string | null
  decision_note: string
  activated_at: string | null
  expires_at: string | null
  revoked_at: string | null
  created_at: string
}

export interface AuditEntry {
  id: number
  band_id: number | null
  band_name: string
  user_id: number | null
  username: string
  acting_grant_id: number | null
  action: string
  entity_type: string
  entity_id: number | null
  details: Record<string, unknown>
  ip_address: string
  created_at: string
}

export interface PlatformSettings {
  maintenance_enabled: boolean
  maintenance_message: string
  announcement_text: string
  announcement_level: string
  announcement_expires_at: string | null
  smtp_enabled: boolean
  smtp_host: string
  smtp_port: number
  smtp_security: string
  smtp_username: string
  smtp_password_set: boolean
  smtp_from: string
  notification_email: string
}

export interface BackupRun {
  id: number
  band_id: number | null
  status: 'running' | 'succeeded' | 'failed'
  trigger: string
  path: string
  size_bytes: number
  error?: string
  started_at: string
  finished_at: string | null
  started_by_username: string
}

export interface SupportMessage {
  id: number
  band_id: number
  band_name?: string
  sender_username: string
  sender_email: string
  message_type: 'issue' | 'question'
  subject: string
  body: string
  assigned_to_user_id: number | null
  assigned_to_username: string
  is_resolved: boolean
  resolved_at: string | null
  resolved_by_username: string
  created_at: string
}

export interface Photo {
  id: number
  variant_id: number | null
  article_name: string
  variant_label: string
  original_filename: string
  position: number
  include_in_slideshow: boolean
  show_price: boolean
  sale_price_cents: number
  size_bytes: number
  created_by_username: string
}

export interface BandUser {
  id: number
  username: string
  role: Role
  role_label: string
  is_active: boolean
  mfa_enabled: boolean
  must_set_password: boolean
  last_login_at: string | null
  created_at: string
  is_self: boolean
}

export interface SupportAssignee {
  id: number
  username: string
  role: 'support_admin' | 'system_admin'
}

export interface PlatformUser extends BandUser {
  contact_email: string
}

export interface ProfilePayload {
  profile: Identity
  available_themes: string[]
  available_languages: string[]
  last_login_at: string | null
  mfa_enrolled_at: string | null
  recovery_codes_left: number
}
