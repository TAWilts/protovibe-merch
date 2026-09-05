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
