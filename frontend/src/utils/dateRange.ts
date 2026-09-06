export function isWithinDateRange(value: string, from: string, to: string): boolean {
  const date = value.trim().slice(0, 10)
  if (!date) return false
  if (from && date < from) return false
  if (to && date > to) return false
  return true
}