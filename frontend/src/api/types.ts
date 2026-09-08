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
  can_create_band_finances: boolean
  can_manage_band_finances: boolean
  can_manage_articles: boolean
  can_manage_slideshow: boolean
  can_use_packing_list: boolean
  can_manage_packing_list: boolean
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
  telemetry_enabled: boolean
  telemetry_decided: boolean
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
}

export type BandRegistrationStatus = 'pending' | 'approved' | 'rejected' | 'expired'

export interface RegistrationCreated {
  reference: string
  status: BandRegistrationStatus
  status_url: string
  expires_at: string
}

export interface PublicRegistrationStatus {
  reference: string
  status: BandRegistrationStatus
  band_name: string
  band_slug: string
  admin_username: string
  contact_email: string
  decision_note: string
  credentials_available: boolean
  credentials_retrieved: boolean
  credentials_available_until: string | null
  expires_at: string
}

export interface RegistrationCredentials {
  band_slug: string
  username: string
  setup_code: string
  setup_code_expires_at: string
}

export interface BandRegistrationRequest {
  id: number
  reference: string
  requested_band_name: string
  requested_band_slug: string
  requested_admin_username: string
  requested_contact_email: string
  band_name: string
  band_slug: string
  admin_username: string
  contact_email: string
  status: BandRegistrationStatus
  privacy_accepted_at: string
  decision_note?: string
  decided_by_username?: string
  decided_at?: string
  band_id?: number
  admin_user_id?: number
  credentials_available_until?: string
  claimed_at?: string
  expires_at: string
  created_at: string
  updated_at: string
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
  total_paid_cents: number
  discount_cents: number
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
  shipping_cost_cents: number
  discount_cents: number
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
  discount_cents: number
  donation_cents: number
  shipping_cost_cents: number
  is_fully_cancelled: boolean
  positions: Position[]
}

export interface Queues {
  open_shipments: Position[]
  delivered_shipments: Position[]
  open_payments: Position[]
  settled_payments: Position[]
}

export interface ShippingAdjustment {
  receipt_id: string
  sale_ids: number[]
  old_shipping_cost_cents: number
  shipping_cost_cents: number
  total_due_cents: number
  total_paid_cents: number
  discount_cents: number
  donation_cents: number
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
  discount_cents: number
  donation_cents: number
  sale_price_cents: number
  is_offered: boolean
  is_available_for_sale: boolean
  no_reorder: boolean
  is_active: boolean
}

export interface BalanceSummary {
  purchase_cost_cents: number
  revenue_cents: number
  collected_cents: number
  discount_cents: number
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

export interface EventTimelinePoint {
  key: string
  label: string
  date: string
  quantity: number
  income_cents: number
  profit_cents: number
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
  event_timeline: EventTimelinePoint[]
}

export interface FinanceReportSummary {
  merch_revenue_cents: number
  merch_collected_cents: number
  discount_cents: number
  donation_cents: number
  merch_purchase_cost_cents: number
  band_income_cents: number
  band_expense_cents: number
  band_open_income_cents: number
  band_open_expense_cents: number
  outstanding_customer_cents: number
  cash_result_cents: number
  stock_value_cents: number
  asset_acquisition_cents: number
}

export interface FinancePaymentMethod {
  payment_method: string
  receipt_count: number
  booked_cents: number
  collected_cents: number
}

export interface FinanceCategory {
  transaction_type: 'income' | 'expense'
  category: string
  amount_cents: number
}

export interface FinanceInventoryRow {
  article_name: string
  variant_label: string
  on_hand: number
  unit_cost_cents: number
  value_cents: number
}

export interface FinanceAssetRow {
  date: string
  category: string
  description: string
  amount_cents: number
}

export interface FinanceReport {
  band_name: string
  from: string
  to: string
  stock_as_of: string
  generated_at: string
  summary: FinanceReportSummary
  payment_methods: FinancePaymentMethod[]
  categories: FinanceCategory[]
  inventory: FinanceInventoryRow[]
  assets: FinanceAssetRow[]
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
  has_receipt_attachment: boolean
  prices_include_vat: boolean
  vat_rate_basis_points: number
  shipping_cost_cents: number
  comment: string
  is_cancelled: boolean
  cancelled_at?: string
  cancelled_by_username: string
  created_by_username: string
}

export interface BandTransaction {
  id: number
  transaction_type: 'income' | 'expense'
  transaction_on: string
  category: string
  description: string
  amount_cents: number
  is_settled: boolean
  is_asset: boolean
  settled_at?: string
  settled_by_username: string
  is_cancelled: boolean
  created_by_username: string
}

export interface RecurringBandTransaction {
  id: number
  transaction_type: 'income' | 'expense'
  start_on: string
  next_run_on: string
  category: string
  description: string
  amount_cents: number
  is_settled: boolean
  is_asset: boolean
  interval_value: number
  interval_unit: 'day' | 'week' | 'month' | 'year'
  is_active: boolean
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
  suggested_income_categories: string[]
  suggested_expense_categories: string[]
  income_cents: number
  expense_cents: number
  balance_cents: number
  open_income_cents: number
  open_expense_cents: number
}

export interface FeatureFlags {
  slideshow?: boolean
  band_finances?: boolean
  payment_qr?: boolean
  offline_sales?: boolean
  csv_import?: boolean
  packing_list?: boolean
}

export type PackingStatus = 'open' | 'packed' | 'stays_here'

export interface PackingPhoto {
  id: string
  bag_id?: string
  item_id?: string
  original_filename: string
  size_bytes: number
  position: number
}

export interface PackingItem {
  id: string
  bag_id: string
  name: string
  position: number
  status: PackingStatus
  photos: PackingPhoto[]
}

export interface PackingBag {
  id: string
  name: string
  position: number
  status: PackingStatus
  photos: PackingPhoto[]
  items: PackingItem[]
}

export interface PackingSnapshot {
  revision: number
  generation: number
  bags: PackingBag[]
  replayed?: boolean
}

export type PackingOperationType =
  | 'create_bag' | 'rename_bag' | 'delete_bag' | 'reorder_bags'
  | 'create_item' | 'rename_item' | 'delete_item' | 'reorder_items'
  | 'set_bag_status' | 'set_item_status' | 'delete_photo' | 'reset'

export interface PackingOperation {
  event_id: string
  device_id: string
  client_created_at: string
  base_generation: number
  type: PackingOperationType
  bag_id?: string
  item_id?: string
  photo_id?: string
  name?: string
  status?: PackingStatus
  order_ids?: string[]
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
  effective_storage_quota_bytes: number
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
