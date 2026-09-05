export interface TelemetryDaily {
  id: number
  day: string
  event_kind: string
  dimension: string
  sample_count: number
  total_duration_ms: number
  total_request_bytes: number
  total_response_bytes: number
  updated_at: string
}

export interface TelemetryEvent {
  occurred_at: string
  event_type: string
  band_alias: string
  operation_alias: string
  subject_alias: string
  feature_key: string
  role: string
  quantity?: number
  unit_price_cents?: number
  amount_cents?: number
  payment_method: string
  is_paid?: boolean
  is_received?: boolean
  is_open?: boolean
  status: string
  delivery_status: string
  payment_follow_up?: boolean
  location: string
  storage_bytes?: number
  http_status: number
  request_bytes: number
  response_bytes: number
  duration_ms: number
}

export interface TelemetryPayload {
  days: number
  since: string
  rows: TelemetryDaily[]
  events: TelemetryEvent[]
}
