import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

/**
 * Money helpers.
 *
 * Every amount crossing the API is an integer number of cents; only the
 * display and the input parsing convert. That mirrors the backend and keeps
 * rounding errors impossible.
 */
export function useMoney() {
  const { locale } = useI18n()

  const formatter = computed(
    () =>
      new Intl.NumberFormat(locale.value === 'en' ? 'en-GB' : 'de-DE', {
        style: 'currency',
        currency: 'EUR',
      }),
  )

  function format(cents: number): string {
    return formatter.value.format(cents / 100)
  }

  return { format, parseAmount }
}

/**
 * Parses a typed amount into cents.
 *
 * It accepts both "18,50" and "18.50". A single separator followed by exactly
 * three digits is refused rather than guessed, matching the backend: "1.234"
 * could be 1234 or 1.23, and misbooking money silently is worse than asking
 * the seller to be explicit.
 */
export function parseAmount(input: string): number | null {
  let cleaned = input.trim().replace(/[€\s  ]/g, '')
  if (!cleaned) return null

  const negative = cleaned.startsWith('-')
  cleaned = cleaned.replace(/^[+-]/, '')

  const lastComma = cleaned.lastIndexOf(',')
  const lastDot = cleaned.lastIndexOf('.')

  if (lastComma >= 0 && lastDot >= 0) {
    // Whichever separator comes last is the decimal one.
    if (lastComma > lastDot) {
      cleaned = cleaned.replace(/\./g, '').replace(',', '.')
    } else {
      cleaned = cleaned.replace(/,/g, '')
    }
  } else if (lastComma >= 0) {
    cleaned = cleaned.replace(',', '.')
  }

  if ((cleaned.match(/\./g) ?? []).length > 1) return null

  const dot = cleaned.lastIndexOf('.')
  if (dot >= 0 && cleaned.length - dot - 1 === 3) return null
  if (!/^\d*(\.\d{1,2})?$/.test(cleaned)) return null

  const [whole = '0', fraction = ''] = cleaned.split('.')
  const cents = Number(whole || '0') * 100 + Number(fraction.padEnd(2, '0') || '0')
  return negative ? -cents : cents
}
