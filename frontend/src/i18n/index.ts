import { createI18n } from 'vue-i18n'
import de from './de.json'
import en from './en.json'

export const SUPPORTED_LOCALES = ['de', 'en'] as const
export type Locale = (typeof SUPPORTED_LOCALES)[number]

/**
 * German is the product language; English exists so a second locale costs only
 * a JSON file. Every user-visible string belongs here, never in a component.
 */
export const i18n = createI18n({
  legacy: false,
  locale: 'de',
  fallbackLocale: 'de',
  messages: { de, en },
  numberFormats: {
    de: { currency: { style: 'currency', currency: 'EUR' } },
    en: { currency: { style: 'currency', currency: 'EUR' } },
  },
  datetimeFormats: {
    de: {
      short: { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' },
      time: { hour: '2-digit', minute: '2-digit' },
    },
    en: {
      short: { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' },
      time: { hour: '2-digit', minute: '2-digit' },
    },
  },
})

export function setLocale(locale: Locale) {
  i18n.global.locale.value = locale
  document.documentElement.lang = locale
}

const MARKETING_LOCALE_KEY = 'merch-marketing-locale'

export function marketingLocale(): Locale {
  if (typeof window === 'undefined') return 'de'
  try {
    const stored = window.localStorage.getItem(MARKETING_LOCALE_KEY)
    if (stored === 'de' || stored === 'en') return stored
  } catch {
    // Private browsing may deny storage; browser language still works.
  }
  return window.navigator.language.toLowerCase().startsWith('en') ? 'en' : 'de'
}

export function setMarketingLocale(locale: Locale) {
  if (typeof window !== 'undefined') {
    try {
      window.localStorage.setItem(MARKETING_LOCALE_KEY, locale)
    } catch {
      // Persisting the preference is optional; the current page still changes.
    }
  }
  setLocale(locale)
}
