<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'

import { ApiError } from '@/api/client'
import { registrationApi } from '@/api/endpoints'
import type { PublicRegistrationStatus, RegistrationCredentials } from '@/api/types'
import { marketingLocale, setMarketingLocale, type Locale } from '@/i18n'
import { useSessionStore } from '@/stores/session'

const TOKEN_STORAGE_KEY = 'merch-registration-token'

const { t, d } = useI18n()
const session = useSessionStore()
const locale = ref<Locale>(marketingLocale())
setMarketingLocale(locale.value)

const registrationEnabled = ref<boolean | null>(null)
const configError = ref(false)
const busy = ref(false)
const statusBusy = ref(false)
const error = ref('')
const notice = ref('')
const token = ref('')
const statusUrl = ref('')
const registrationStatus = ref<PublicRegistrationStatus | null>(null)
const credentials = ref<RegistrationCredentials | null>(null)
const pageActive = ref(!document.hidden)

const form = reactive({
  band_name: '',
  band_slug: '',
  admin_username: '',
  contact_email: '',
  privacy_accepted: false,
  website: '',
})

const features = ['mobile', 'payments', 'inventory', 'roles', 'backups', 'support'] as const
const steps = ['request', 'review', 'start'] as const
const faqs = ['email', 'approval', 'link'] as const

const appTarget = computed(() => {
  if (!session.isAuthenticated) return { name: 'login' }
  const caps = session.capabilities
  if (caps?.is_platform_staff) {
    return { name: 'platform-bands' }
  }
  return { name: 'sales' }
})

const appLabel = computed(() => {
  if (!session.isAuthenticated) return t('landing.nav.login')
  const caps = session.capabilities
  return caps?.is_platform_staff
    ? t('landing.nav.toAdmin')
    : t('landing.nav.toSales')
})

const statusLink = computed(() => {
  if (statusUrl.value) return statusUrl.value
  if (!token.value) return ''
  return `${window.location.origin}/#registration=${token.value}`
})

const vAnimate = {
  mounted(element: HTMLElement) {
    if (!('IntersectionObserver' in window)) {
      element.classList.add('is-visible')
      return
    }
    const observer = new IntersectionObserver((entries) => {
      for (const entry of entries) {
        element.classList.toggle('is-visible', entry.isIntersecting)
      }
    }, { threshold: 0.18 })
    observer.observe(element)
    ;(element as HTMLElement & { _landingObserver?: IntersectionObserver })._landingObserver = observer
  },
  unmounted(element: HTMLElement) {
    ;(element as HTMLElement & { _landingObserver?: IntersectionObserver })._landingObserver?.disconnect()
  },
}

function chooseLocale(next: Locale) {
  locale.value = next
  setMarketingLocale(next)
}

function onVisibilityChange() {
  pageActive.value = !document.hidden
}

function tokenFromHash(): string {
  return new URLSearchParams(window.location.hash.replace(/^#/, '')).get('registration') ?? ''
}

function tokenFromStatusUrl(url: string): string {
  try {
    return new URLSearchParams(new URL(url).hash.replace(/^#/, '')).get('registration') ?? ''
  } catch {
    return ''
  }
}

function rememberToken(nextToken: string, url = '') {
  token.value = nextToken
  statusUrl.value = url
  try {
    window.localStorage.setItem(TOKEN_STORAGE_KEY, nextToken)
  } catch {
    // The visible copyable link remains the fallback when storage is denied.
  }
  window.history.replaceState(null, '', `/#registration=${nextToken}`)
}

function forgetToken(removeFragment = true) {
  try {
    window.localStorage.removeItem(TOKEN_STORAGE_KEY)
  } catch {
    // Storage is only a convenience; the fragment is authoritative.
  }
  if (removeFragment && tokenFromHash()) window.history.replaceState(null, '', '/')
}

function describeError(caught: unknown): string {
  if (caught instanceof ApiError) {
    const key = `landing.registration.errors.${caught.detailCode ?? 'generic'}`
    return t(key, caught.message)
  }
  return t('landing.registration.errors.network')
}

async function loadConfig() {
  try {
    registrationEnabled.value = (await registrationApi.config()).registration_enabled
  } catch {
    configError.value = true
  }
}

async function refreshStatus() {
  if (!token.value) return
  statusBusy.value = true
  error.value = ''
  try {
    registrationStatus.value = await registrationApi.status(token.value)
    if (registrationStatus.value.status === 'rejected' || registrationStatus.value.status === 'expired') {
      forgetToken(false)
    }
  } catch (caught) {
    error.value = describeError(caught)
  } finally {
    statusBusy.value = false
  }
}

async function submitRegistration() {
  if (busy.value) return
  busy.value = true
  error.value = ''
  notice.value = ''
  try {
    const created = await registrationApi.create({
      band_name: form.band_name.trim(),
      band_slug: form.band_slug.trim().toLowerCase(),
      admin_username: form.admin_username.trim(),
      contact_email: form.contact_email.trim(),
      privacy_accepted: form.privacy_accepted,
      website: form.website,
    })
    const nextToken = tokenFromStatusUrl(created.status_url)
    if (!nextToken) throw new Error('missing status token')
    rememberToken(nextToken, created.status_url)
    notice.value = t('landing.registration.created')
    await refreshStatus()
  } catch (caught) {
    error.value = describeError(caught)
  } finally {
    busy.value = false
  }
}

async function claimCredentials() {
  if (!token.value || busy.value) return
  busy.value = true
  error.value = ''
  try {
    credentials.value = await registrationApi.claim(token.value)
    if (registrationStatus.value) {
      registrationStatus.value.credentials_available = false
      registrationStatus.value.credentials_retrieved = true
    }
    forgetToken(true)
  } catch (caught) {
    error.value = describeError(caught)
    await refreshStatus()
  } finally {
    busy.value = false
  }
}

async function copy(value: string, message: string) {
  try {
    await navigator.clipboard.writeText(value)
    notice.value = message
  } catch {
    error.value = t('landing.registration.copyFailed')
  }
}

function credentialText(): string {
  if (!credentials.value) return ''
  return [
    `${t('landing.registration.bandSlug')}: ${credentials.value.band_slug}`,
    `${t('landing.registration.adminUsername')}: ${credentials.value.username}`,
    `${t('landing.registration.setupCode')}: ${credentials.value.setup_code}`,
    `${t('landing.registration.validUntil')}: ${d(new Date(credentials.value.setup_code_expires_at), 'short')}`,
  ].join('\n')
}

function downloadCredentials() {
  const content = credentialText()
  if (!content) return
  const url = URL.createObjectURL(new Blob([content], { type: 'text/plain;charset=utf-8' }))
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = 'merch-manager-zugang.txt'
  anchor.click()
  URL.revokeObjectURL(url)
}

function startOver() {
  forgetToken(true)
  token.value = ''
  statusUrl.value = ''
  registrationStatus.value = null
  credentials.value = null
  error.value = ''
  notice.value = ''
}

onMounted(async () => {
  document.addEventListener('visibilitychange', onVisibilityChange)
  void loadConfig()
  let storedToken = ''
  try {
    storedToken = window.localStorage.getItem(TOKEN_STORAGE_KEY) || ''
  } catch {
    // Continue with the URL fragment when browser storage is unavailable.
  }
  const resumedToken = tokenFromHash() || storedToken
  if (resumedToken) {
    rememberToken(resumedToken)
    await refreshStatus()
    document.querySelector('#register')?.scrollIntoView({ behavior: 'smooth' })
  }
})

onBeforeUnmount(() => document.removeEventListener('visibilitychange', onVisibilityChange))
</script>

<template>
  <div class="landing-page" :class="{ 'animations-paused': !pageActive }">
    <header class="landing-header">
      <a class="landing-brand" href="#top" aria-label="Merch Manager">
        <span class="brand-mark">M</span>
        <span>Merch Manager</span>
      </a>
      <nav class="landing-nav" :aria-label="t('landing.nav.label')">
        <a href="#features">{{ t('landing.nav.features') }}</a>
        <a href="#workflow">{{ t('landing.nav.workflow') }}</a>
        <a href="#register">{{ t('landing.nav.register') }}</a>
      </nav>
      <div class="landing-actions">
        <div class="landing-locale" :aria-label="t('landing.language.label')">
          <button type="button" :class="{ active: locale === 'de' }" @click="chooseLocale('de')">DE</button>
          <button type="button" :class="{ active: locale === 'en' }" @click="chooseLocale('en')">EN</button>
        </div>
        <RouterLink class="landing-button landing-button-small landing-button-ghost" :to="appTarget">
          {{ appLabel }}
        </RouterLink>
      </div>
    </header>

    <main id="top">
      <section class="landing-section hero-section">
        <div class="hero-copy">
          <p class="landing-kicker">{{ t('landing.hero.kicker') }}</p>
          <h1>{{ t('landing.hero.title') }}</h1>
          <p class="hero-lead">{{ t('landing.hero.lead') }}</p>
          <div class="hero-actions">
            <a class="landing-button landing-button-primary" href="#register">{{ t('landing.hero.register') }}</a>
            <RouterLink class="landing-button landing-button-ghost" :to="{ name: 'login' }">
              {{ t('landing.hero.login') }}
            </RouterLink>
          </div>
          <div class="hero-trust">
            <span>✓ {{ t('landing.hero.mobile') }}</span>
            <span>✓ {{ t('landing.hero.noInstall') }}</span>
            <span>✓ {{ t('landing.hero.onAndOffline') }}</span>
            <span>✓ {{ t('landing.hero.noVendorLockin') }}</span>
          </div>
        </div>

        <div v-animate class="app-showcase is-visible" aria-hidden="true">
          <div class="showcase-glow"></div>
          <div class="demo-window hero-window">
            <div class="demo-titlebar">
              <span class="demo-logo">M</span><strong>Merch Manager</strong>
              <span class="demo-event">Live · Tour 2026</span>
            </div>
            <div class="hero-app-grid">
              <div class="demo-products">
                <span class="demo-label">{{ t('landing.demo.article') }}</span>
                <button class="demo-product active"><i class="shirt-icon"></i><span>Tour Shirt<small>25,00 €</small></span></button>
                <button class="demo-product"><i class="record-icon"></i><span>Vinyl<small>22,00 €</small></span></button>
                <button class="demo-product"><i class="bag-icon"></i><span>Tote Bag<small>15,00 €</small></span></button>
              </div>
              <div class="demo-options">
                <span class="demo-label">{{ t('landing.demo.variant') }}</span>
                <strong>Tour Shirt</strong>
                <small>{{ t('landing.demo.size') }}</small>
                <div class="demo-pills"><span>S</span><span class="selected">M</span><span>L</span><span>XL</span></div>
                <small>{{ t('landing.demo.color') }}</small>
                <div class="demo-swatches"><span></span><span class="active"></span><span></span></div>
                <button class="demo-add">+ {{ t('landing.demo.add') }}</button>
              </div>
              <div class="demo-cart">
                <span class="demo-label">{{ t('landing.demo.cart') }}</span>
                <div><strong>Tour Shirt · M</strong><span>25,00 €</span></div>
                <div><strong>Tote Bag</strong><span>15,00 €</span></div>
                <div class="demo-total"><span>{{ t('landing.demo.total') }}</span><strong>40,00 €</strong></div>
                <button>{{ t('landing.demo.continue') }} →</button>
              </div>
            </div>
          </div>
        </div>
      </section>

      <section id="features" class="landing-section demos-section">
        <div class="landing-heading">
          <p class="landing-kicker">{{ t('landing.demos.kicker') }}</p>
          <h2>{{ t('landing.demos.title') }}</h2>
          <p>{{ t('landing.demos.lead') }}</p>
        </div>

        <article v-animate class="story-row demo-animated">
          <div class="story-copy">
            <span class="story-number">01</span>
            <h3>{{ t('landing.demos.sales.title') }}</h3>
            <p>{{ t('landing.demos.sales.text') }}</p>
          </div>
          <div class="demo-window wizard-demo" aria-hidden="true">
            <div class="wizard-steps">
                <i class="wizard-step-one"></i>
                <i class="wizard-step-two"></i>
                <i class="wizard-step-three"></i>
            </div>

            <!-- Fenster 1: Artikel -->
            <div class="wizard-slide slide-one">
                <span class="demo-label">1 · {{ t('landing.demo.article') }}</span>

                <div class="mini-product-grid">
                <span class="wizard-product wizard-shirt">Shirt</span>
                <span class="wizard-product wizard-vinyl">Vinyl</span>
                <span>Bag</span>
                </div>

                <div class="mini-cart wizard-cart">
                <div class="cart-state cart-state-zero">
                    <b>0 {{ t('landing.demo.items') }}</b>
                    <strong>0,00 €</strong>
                </div>

                <div class="cart-state cart-state-one">
                    <b>1 {{ t('landing.demo.items') }}</b>
                    <strong>25,00 €</strong>
                </div>

                <div class="cart-state cart-state-two">
                    <b>2 {{ t('landing.demo.items') }}</b>
                    <strong>45,00 €</strong>
                </div>
                </div>
            </div>

            <!-- Fenster 2: Zahlung -->
            <div class="wizard-slide slide-two">
                <span class="demo-label">2 · {{ t('landing.demo.payment') }}</span>

                <div class="payment-grid">
                <span>Cash</span>
                <span class="wizard-paypal">PayPal</span>
                <span>Bank</span>
                </div>
            </div>

            <!-- Fenster 3: Abschluss -->
            <div class="wizard-slide slide-three">
                <span class="demo-label">3 · {{ t('landing.demo.done') }}</span>

                <div class="wizard-qr">
                <div class="fake-qr"></div>
                <strong>45,00 €</strong>
                <small>VK-2026-0042</small>
                </div>

                <button class="payment-received-button">
                Zahlung erhalten
                </button>

                <div class="purchase-success">
                <span>✓</span>
                <strong>Kauf erfolgreich</strong>
                </div>
            </div>
            </div>
        </article>

        <article v-animate class="story-row story-reverse demo-animated">
          <div class="story-copy">
            <span class="story-number">02</span>
            <h3>{{ t('landing.demos.inventory.title') }}</h3>
            <p>{{ t('landing.demos.inventory.text') }}</p>
          </div>
          <div class="demo-window inventory-demo" aria-hidden="true">
            <div class="inventory-head"><strong>{{ t('landing.demo.stock') }}</strong><span>{{ t('landing.demo.live') }}</span></div>
            <div class="stock-row"><span>Tour Shirt M</span><i><b style="--stock: 78%"></b></i><strong>18</strong></div>
            <div class="stock-row warning"><span>Tour Shirt L</span><i><b style="--stock: 24%"></b></i><strong>3</strong></div>
            <div class="stock-row"><span>Vinyl</span><i><b style="--stock: 58%"></b></i><strong>12</strong></div>
            <div class="stock-event"><span>− 1</span><b>Tour Shirt L</b><small>{{ t('landing.demo.justNow') }}</small></div>
          </div>
        </article>

        <article v-animate class="story-row demo-animated">
          <div class="story-copy">
            <span class="story-number">03</span>
            <h3>{{ t('landing.demos.slideshow.title') }}</h3>
            <p>{{ t('landing.demos.slideshow.text') }}</p>
          </div>
          <div class="demo-window mosaic-demo" aria-hidden="true">
            <div class="mosaic-track">
              <div class="merch-tile tile-shirt"><i class="shirt-icon"></i><span>25 €</span></div>
              <div class="merch-tile tile-record"><i class="record-icon"></i></div>
              <div class="merch-tile tile-bag"><i class="bag-icon"></i><span>15 €</span></div>
              <div class="merch-tile tile-cap"><i>★</i></div>
              <div class="merch-tile tile-shirt alt"><i class="shirt-icon"></i></div>
              <div class="merch-tile tile-record alt"><i class="record-icon"></i><span>22 €</span></div>
            </div>
          </div>
        </article>
      </section>

      <section class="landing-section feature-section">
        <div class="landing-heading">
          <p class="landing-kicker">{{ t('landing.features.kicker') }}</p>
          <h2>{{ t('landing.features.title') }}</h2>
        </div>
        <div class="feature-grid">
          <article v-for="(feature, index) in features" :key="feature" v-animate class="feature-card">
            <span class="feature-icon">{{ ['⌁', '◈', '↗', '◎', '◇', '✦'][index] }}</span>
            <h3>{{ t(`landing.features.${feature}.title`) }}</h3>
            <p>{{ t(`landing.features.${feature}.text`) }}</p>
          </article>
        </div>
      </section>

      <section id="workflow" class="landing-section workflow-section">
        <div class="landing-heading">
          <p class="landing-kicker">{{ t('landing.workflow.kicker') }}</p>
          <h2>{{ t('landing.workflow.title') }}</h2>
          <p>{{ t('landing.workflow.lead') }}</p>
        </div>
        <ol class="workflow-grid">
          <li v-for="(step, index) in steps" :key="step" v-animate>
            <span>{{ String(index + 1).padStart(2, '0') }}</span>
            <h3>{{ t(`landing.workflow.${step}.title`) }}</h3>
            <p>{{ t(`landing.workflow.${step}.text`) }}</p>
          </li>
        </ol>
      </section>

      <section id="register" class="landing-section registration-section">
        <div class="registration-intro">
          <p class="landing-kicker">{{ t('landing.registration.kicker') }}</p>
          <h2>{{ t('landing.registration.title') }}</h2>
          <p>{{ t('landing.registration.lead') }}</p>
          <div class="registration-note">
            <strong>{{ t('landing.registration.noEmailTitle') }}</strong>
            <span>{{ t('landing.registration.noEmailText') }}</span>
          </div>
        </div>

        <div class="registration-card" aria-live="polite">
          <div v-if="error" class="landing-alert error">{{ error }}</div>
          <div v-if="notice" class="landing-alert success">{{ notice }}</div>

          <div v-if="credentials" class="credential-panel">
            <span class="status-orb approved">✓</span>
            <p class="landing-kicker">{{ t('landing.registration.approved') }}</p>
            <h3>{{ t('landing.registration.credentialsTitle') }}</h3>
            <p>{{ t('landing.registration.credentialsOnce') }}</p>
            <dl>
              <div><dt>{{ t('landing.registration.bandSlug') }}</dt><dd><code>{{ credentials.band_slug }}</code></dd></div>
              <div><dt>{{ t('landing.registration.adminUsername') }}</dt><dd><code>{{ credentials.username }}</code></dd></div>
              <div class="setup-code"><dt>{{ t('landing.registration.setupCode') }}</dt><dd><code>{{ credentials.setup_code }}</code></dd></div>
              <div><dt>{{ t('landing.registration.validUntil') }}</dt><dd>{{ d(new Date(credentials.setup_code_expires_at), 'short') }}</dd></div>
            </dl>
            <div class="registration-actions">
              <button class="landing-button landing-button-ghost" type="button" @click="copy(credentialText(), t('landing.registration.credentialsCopied'))">
                {{ t('landing.registration.copyCredentials') }}
              </button>
              <button class="landing-button landing-button-ghost" type="button" @click="downloadCredentials">
                {{ t('landing.registration.download') }}
              </button>
              <RouterLink class="landing-button landing-button-primary" :to="{ name: 'login', query: { band: credentials.band_slug, username: credentials.username } }">
                {{ t('landing.registration.toLogin') }}
              </RouterLink>
            </div>
          </div>

          <div v-else-if="registrationStatus" class="status-panel">
            <span class="status-orb" :class="registrationStatus.status">
              {{ registrationStatus.status === 'approved' ? '✓' : registrationStatus.status === 'rejected' ? '×' : registrationStatus.status === 'expired' ? '!' : '…' }}
            </span>
            <p class="landing-kicker">{{ registrationStatus.reference }}</p>
            <h3>{{ t(`landing.registration.status.${registrationStatus.status}.title`) }}</h3>
            <p>{{ t(`landing.registration.status.${registrationStatus.status}.text`) }}</p>
            <dl class="status-details">
              <div><dt>{{ t('landing.registration.bandName') }}</dt><dd>{{ registrationStatus.band_name }}</dd></div>
              <div><dt>{{ t('landing.registration.bandSlug') }}</dt><dd><code>{{ registrationStatus.band_slug }}</code></dd></div>
              <div><dt>{{ t('landing.registration.adminUsername') }}</dt><dd><code>{{ registrationStatus.admin_username }}</code></dd></div>
              <div><dt>{{ t('landing.registration.validUntil') }}</dt><dd>{{ d(new Date(registrationStatus.expires_at), 'short') }}</dd></div>
            </dl>
            <p v-if="registrationStatus.decision_note" class="decision-note">{{ registrationStatus.decision_note }}</p>
            <p v-if="registrationStatus.credentials_retrieved" class="landing-alert warning">
              {{ t('landing.registration.alreadyClaimed') }}
            </p>
            <div v-if="registrationStatus.status === 'pending'" class="resume-link">
              <label>{{ t('landing.registration.statusLink') }}<input :value="statusLink" readonly /></label>
              <button class="landing-button landing-button-ghost" type="button" @click="copy(statusLink, t('landing.registration.linkCopied'))">
                {{ t('landing.registration.copyLink') }}
              </button>
            </div>
            <div class="registration-actions">
              <button v-if="registrationStatus.credentials_available" class="landing-button landing-button-primary" type="button" :disabled="busy" @click="claimCredentials">
                {{ t('landing.registration.showCredentials') }}
              </button>
              <button v-if="registrationStatus.status === 'pending'" class="landing-button landing-button-ghost" type="button" :disabled="statusBusy" @click="refreshStatus">
                {{ t('landing.registration.refresh') }}
              </button>
              <button v-if="registrationStatus.status === 'rejected' || registrationStatus.status === 'expired'" class="landing-button landing-button-ghost" type="button" @click="startOver">
                {{ t('landing.registration.newRequest') }}
              </button>
            </div>
          </div>

          <div v-else-if="statusBusy" class="registration-loading">{{ t('common.loading') }}</div>

          <div v-else-if="registrationEnabled === false" class="registration-unavailable">
            <span class="status-orb">i</span>
            <h3>{{ t('landing.registration.disabledTitle') }}</h3>
            <p>{{ t('landing.registration.disabledText') }}</p>
          </div>

          <div v-else-if="configError" class="registration-unavailable">
            <h3>{{ t('landing.registration.configErrorTitle') }}</h3>
            <p>{{ t('landing.registration.configErrorText') }}</p>
          </div>

          <form v-else class="registration-form" @submit.prevent="submitRegistration">
            <div class="registration-form-heading">
              <span>01</span>
              <div><h3>{{ t('landing.registration.formTitle') }}</h3><p>{{ t('landing.registration.formText') }}</p></div>
            </div>
            <label>{{ t('landing.registration.bandName') }}<input v-model="form.band_name" maxlength="200" autocomplete="organization" required /></label>
            <label>
              {{ t('landing.registration.bandSlug') }}
              <span class="slug-field"><span>/</span><input v-model="form.band_slug" minlength="2" maxlength="64" pattern="[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])" autocomplete="off" required /></span>
              <small>{{ t('landing.registration.bandSlugHint') }}</small>
            </label>
            <div class="registration-columns">
              <label>{{ t('landing.registration.adminUsername') }}<input v-model="form.admin_username" minlength="3" maxlength="150" autocomplete="username" required /></label>
              <label>{{ t('landing.registration.contactEmail') }}<input v-model="form.contact_email" type="email" maxlength="254" autocomplete="email" required /></label>
            </div>
            <label class="honeypot" aria-hidden="true">Website<input v-model="form.website" tabindex="-1" autocomplete="off" /></label>
            <label class="privacy-check">
              <input v-model="form.privacy_accepted" type="checkbox" required />
              <span>{{ t('landing.registration.privacy') }}</span>
            </label>
            <button class="landing-button landing-button-primary submit-registration" type="submit" :disabled="busy || registrationEnabled === null">
              {{ busy ? t('landing.registration.sending') : t('landing.registration.submit') }}
            </button>
          </form>
        </div>
      </section>

      <section class="landing-section faq-section">
        <div class="landing-heading"><p class="landing-kicker">FAQ</p><h2>{{ t('landing.faq.title') }}</h2></div>
        <div class="faq-list">
          <details v-for="faq in faqs" :key="faq">
            <summary>{{ t(`landing.faq.${faq}.question`) }}</summary>
            <p>{{ t(`landing.faq.${faq}.answer`) }}</p>
          </details>
        </div>
      </section>
    </main>

    <footer class="landing-footer">
      <a class="landing-brand" href="#top"><span class="brand-mark">M</span><span>Merch Manager</span></a>
      <p>{{ t('landing.footer') }}</p>
      <RouterLink :to="{ name: 'login' }">{{ t('landing.nav.login') }} →</RouterLink>
    </footer>
  </div>
</template>

<style scoped>
.landing-page {
  --landing-bg: #0b0710;
  --landing-panel: rgba(27, 20, 36, 0.76);
  --landing-line: rgba(240, 142, 255, 0.16);
  min-height: 100vh;
  overflow: hidden;
  color: #f8f4fb;
  background:
    radial-gradient(circle at 12% 8%, rgba(154, 54, 199, 0.26), transparent 31rem),
    radial-gradient(circle at 89% 28%, rgba(92, 47, 155, 0.2), transparent 30rem),
    var(--landing-bg);
}

.landing-page::before {
  position: fixed;
  inset: 0;
  z-index: 0;
  pointer-events: none;
  content: '';
  opacity: 0.16;
  background-image: linear-gradient(rgba(255,255,255,.035) 1px, transparent 1px), linear-gradient(90deg, rgba(255,255,255,.035) 1px, transparent 1px);
  background-size: 52px 52px;
  mask-image: linear-gradient(to bottom, black, transparent 76%);
}

.landing-header {
  position: fixed;
  top: 14px;
  right: 16px;
  left: 16px;
  z-index: 50;
  max-width: 1240px;
  min-height: 62px;
  margin: auto;
  padding: 8px 10px 8px 16px;
  display: flex;
  align-items: center;
  gap: 26px;
  border: 1px solid rgba(255,255,255,.1);
  border-radius: 18px;
  background: rgba(13, 9, 18, 0.76);
  box-shadow: 0 16px 50px rgba(0,0,0,.22);
  backdrop-filter: blur(18px);
}

.landing-brand { display: inline-flex; align-items: center; gap: 10px; font-weight: 820; letter-spacing: -.035em; white-space: nowrap; }
.landing-brand .brand-mark { width: 32px; height: 32px; }
.landing-nav { display: flex; align-items: center; gap: 4px; }
.landing-nav a { padding: 9px 11px; border-radius: 9px; color: #bfb5c9; font-size: .86rem; font-weight: 650; }
.landing-nav a:hover { color: white; background: rgba(255,255,255,.05); }
.landing-actions { margin-left: auto; display: flex; align-items: center; gap: 9px; }
.landing-locale { display: flex; padding: 3px; border: 1px solid rgba(255,255,255,.1); border-radius: 999px; background: rgba(0,0,0,.2); }
.landing-locale button { min-width: 34px; padding: 5px 7px; border: 0; border-radius: 999px; color: #9f94aa; background: transparent; font-size: .7rem; font-weight: 850; }
.landing-locale button.active { color: #270d30; background: #f08eff; }

.landing-section, .landing-footer { position: relative; z-index: 1; width: min(1180px, calc(100% - 40px)); margin: 0 auto; }
.landing-section { scroll-margin-top: 92px; }
.hero-section { min-height: 100vh; padding: 148px 0 92px; display: grid; grid-template-columns: .9fr 1.2fr; gap: clamp(42px, 7vw, 90px); align-items: center; }
.landing-kicker { margin: 0 0 13px; color: #f19bff; font-size: .73rem; font-weight: 820; letter-spacing: .15em; text-transform: uppercase; }
.hero-copy h1 { max-width: 700px; margin: 0; font-size: clamp(3.1rem, 6.6vw, 6.4rem); line-height: .94; letter-spacing: -.075em; }
.hero-lead { max-width: 590px; margin: 27px 0 0; color: #c7bdcf; font-size: clamp(1rem, 1.4vw, 1.18rem); line-height: 1.65; }
.hero-actions, .registration-actions { display: flex; flex-wrap: wrap; gap: 10px; margin-top: 30px; }
.landing-button { min-height: 44px; padding: 11px 17px; display: inline-flex; align-items: center; justify-content: center; border: 1px solid transparent; border-radius: 10px; font-weight: 790; font-size: .88rem; text-decoration: none; transition: transform .18s, border-color .18s, background .18s; }
.landing-button:hover:not(:disabled) { transform: translateY(-2px); }
.landing-button:focus-visible, .landing-page button:focus-visible, .landing-page a:focus-visible, .landing-page input:focus-visible, .landing-page summary:focus-visible { outline: 3px solid rgba(240,142,255,.38); outline-offset: 3px; }
.landing-button-primary { color: #280c31; background: linear-gradient(135deg, #f5a8ff, #d95ff1); box-shadow: 0 13px 32px rgba(217,95,241,.22); }
.landing-button-ghost { color: #f8f4fb; border-color: rgba(255,255,255,.14); background: rgba(255,255,255,.045); }
.landing-button-small { min-height: 38px; padding: 8px 13px; font-size: .78rem; }
.hero-trust { margin-top: 24px; display: flex; flex-wrap: wrap; gap: 13px 20px; color: #9e93aa; font-size: .76rem; }
.hero-trust span::first-letter { color: #e986f9; }

.app-showcase { position: relative; perspective: 1200px; opacity: 0; transform: translateY(28px); transition: opacity .8s, transform .8s; }
.app-showcase.is-visible { opacity: 1; transform: none; }
.showcase-glow { position: absolute; inset: 12% 8%; filter: blur(55px); background: rgba(208,70,237,.23); }
.demo-window { position: relative; overflow: hidden; border: 1px solid rgba(255,255,255,.13); border-radius: 18px; background: linear-gradient(145deg, rgba(37,28,47,.96), rgba(15,11,21,.96)); box-shadow: 0 35px 90px rgba(0,0,0,.45); }
.hero-window { transform: rotateY(-5deg) rotateX(2deg); }
.demo-titlebar { height: 52px; padding: 0 15px; display: flex; align-items: center; gap: 9px; border-bottom: 1px solid rgba(255,255,255,.08); color: #f4edf7; font-size: .77rem; }
.demo-logo { width: 25px; height: 25px; display: grid; place-items: center; border-radius: 8px; color: #250c2c; background: #e982f9; font-weight: 900; }
.demo-event { margin-left: auto; padding: 5px 8px; border-radius: 99px; color: #8ee9b2; background: rgba(82,209,139,.1); font-size: .61rem; }
.hero-app-grid { min-height: 410px; padding: 13px; display: grid; grid-template-columns: .8fr 1fr .92fr; gap: 10px; }
.hero-app-grid > div { padding: 13px; border: 1px solid rgba(255,255,255,.075); border-radius: 12px; background: rgba(6,4,9,.34); }
.demo-label { display: block; margin-bottom: 12px; color: #93889f; font-size: .58rem; font-weight: 820; letter-spacing: .1em; text-transform: uppercase; }
.demo-products { display: grid; align-content: start; gap: 8px; }
.demo-product { width: 100%; padding: 10px; display: flex; align-items: center; gap: 9px; border: 1px solid rgba(255,255,255,.08); border-radius: 9px; color: white; background: rgba(255,255,255,.035); text-align: left; }
.demo-product.active { border-color: rgba(240,142,255,.7); background: rgba(217,95,241,.15); }
.demo-product i { width: 31px; height: 31px; flex: 0 0 auto; display: grid; place-items: center; border-radius: 7px; background: linear-gradient(145deg,#5b3269,#201626); }
.demo-product span { font-size: .68rem; font-weight: 750; }
.demo-product small { display: block; margin-top: 2px; color: #a99daf; font-size: .56rem; }
.shirt-icon::before { content: 'T'; color: #efd9f4; font-style: normal; font-weight: 900; }
.record-icon::before { content: '●'; color: #efd9f4; font-style: normal; font-size: 1.25rem; }
.bag-icon::before { content: '▢'; color: #efd9f4; font-style: normal; font-size: 1.15rem; }
.demo-options { display: flex; flex-direction: column; }
.demo-options > strong { font-size: .92rem; }
.demo-options > small { margin: 17px 0 7px; color: #94899e; font-size: .6rem; }
.demo-pills { display: flex; gap: 5px; }
.demo-pills span { min-width: 29px; padding: 6px; border: 1px solid rgba(255,255,255,.1); border-radius: 6px; color: #b7adbe; font-size: .61rem; text-align: center; }
.demo-pills .selected { border-color: #ec8bfb; color: white; background: rgba(217,95,241,.18); }
.demo-swatches { display: flex; gap: 7px; }
.demo-swatches span { width: 22px; height: 22px; border: 3px solid #28212e; border-radius: 50%; background: #151219; box-shadow: 0 0 0 1px rgba(255,255,255,.15); }
.demo-swatches span:nth-child(2) { background: #eee7df; }
.demo-swatches span:nth-child(3) { background: #682e72; }
.demo-swatches .active { box-shadow: 0 0 0 2px #e982f9; }
.demo-add { margin-top: auto; padding: 9px; border: 0; border-radius: 7px; color: #260b2e; background: #e982f9; font-size: .65rem; font-weight: 850; }
.demo-cart { display: flex; flex-direction: column; gap: 9px; }
.demo-cart > div { padding-bottom: 9px; display: flex; justify-content: space-between; gap: 6px; border-bottom: 1px solid rgba(255,255,255,.07); font-size: .61rem; }
.demo-cart > div span { color: #d78be3; white-space: nowrap; }
.demo-cart .demo-total { margin-top: auto; padding: 11px 0; align-items: end; }
.demo-total strong { color: #f08eff; font-size: 1rem; }
.demo-cart button { padding: 9px; border: 0; border-radius: 7px; color: #260b2e; background: #e982f9; font-size: .63rem; font-weight: 850; }

.demos-section, .feature-section, .workflow-section, .registration-section, .faq-section { padding: 105px 0; }
.landing-heading { max-width: 670px; margin-bottom: 58px; }
.landing-heading h2, .registration-intro h2 { margin: 0; font-size: clamp(2.2rem, 5vw, 4.4rem); line-height: 1; letter-spacing: -.06em; }
.landing-heading > p:last-child, .registration-intro > p { color: #aea3b7; line-height: 1.65; }
.story-row { min-height: 440px; margin: 0 0 78px; display: grid; grid-template-columns: .75fr 1.25fr; gap: clamp(35px, 9vw, 115px); align-items: center; opacity: .45; transition: opacity .6s; }
.story-row.is-visible { opacity: 1; }
.story-reverse .story-copy { order: 2; }
.story-number { color: #694a73; font-family: ui-monospace, monospace; font-size: .72rem; }
.story-copy h3 { margin: 17px 0 12px; font-size: clamp(1.8rem, 3.5vw, 3rem); letter-spacing: -.045em; }
.story-copy p { margin: 0; color: #aea3b7; line-height: 1.7; }
.wizard-demo, .inventory-demo, .mosaic-demo {
  min-height: 340px;
}

.wizard-demo {
  padding: 23px;
}


/* ---------------------------------------------------------
   Fortschrittsanzeige
   --------------------------------------------------------- */

.wizard-steps {
  display: flex;
  gap: 7px;
  margin-bottom: 26px;
}

.wizard-steps i {
  width: 28px;
  height: 4px;
  border-radius: 9px;
  background: #44394d;
}

.wizard-step-one {
  animation: wizard-step-one 10s infinite;
}

.wizard-step-two {
  animation: wizard-step-two 10s infinite;
}

.wizard-step-three {
  animation: wizard-step-three 10s infinite;
}


/* ---------------------------------------------------------
   Die drei Hauptfenster
   --------------------------------------------------------- */

.wizard-slide {
  position: absolute;
  inset: 58px 23px 23px;

  display: flex;
  flex-direction: column;

  opacity: 0;
  transform: translateX(25px);
  pointer-events: none;
}

.slide-one {
  animation: wizard-slide-one 10s infinite;
}

.slide-two {
  animation: wizard-slide-two 10s infinite;
}

.slide-three {
  align-items: center;
  justify-content: center;
  animation: wizard-slide-three 10s infinite;
}


/* ---------------------------------------------------------
   Artikel- und Zahlungsbuttons
   --------------------------------------------------------- */

.mini-product-grid,
.payment-grid {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 10px;
}

.mini-product-grid span,
.payment-grid span {
  min-height: 105px;

  display: grid;
  place-items: center;

  border: 1px solid rgba(255,255,255,.1);
  border-radius: 11px;

  color: #d9cedf;
  background: rgba(255,255,255,.04);

  font-weight: 750;
}

.payment-grid span {
  min-height: 130px;
}


/* Vinyl wird in Phase 2 ausgewählt */

.wizard-vinyl {
  animation: wizard-vinyl-highlight 10s infinite;
}


/* Danach Shirt */

.wizard-shirt {
  animation: wizard-shirt-highlight 10s infinite;
}


/* PayPal in Fenster 2 */

.wizard-paypal {
  animation: wizard-paypal-highlight 10s infinite;
}


/* ---------------------------------------------------------
   Warenkorb
   --------------------------------------------------------- */

.wizard-cart {
  position: relative;
  min-height: 52px;
  margin-top: auto;
}

.cart-state {
  position: absolute;
  inset: 0;

  padding: 15px;

  display: flex;
  justify-content: space-between;
  align-items: center;

  border-radius: 10px;
  background: rgba(217,95,241,.12);

  opacity: 0;
}

.cart-state strong {
  color: #f08eff;
}

.cart-state-zero {
  animation: cart-state-zero 10s infinite;
}

.cart-state-one {
  animation: cart-state-one 10s infinite;
}

.cart-state-two {
  animation: cart-state-two 10s infinite;
}


/* ---------------------------------------------------------
   Fenster 3
   --------------------------------------------------------- */

.wizard-qr {
  display: flex;
  flex-direction: column;
  align-items: center;

  opacity: 0;
  transform: scale(.92);

  animation: wizard-qr-show 10s infinite;
}

.fake-qr {
  width: 130px;
  height: 130px;
  margin-bottom: 12px;

  border: 8px solid white;
  border-radius: 5px;

  background:
    repeating-conic-gradient(
      #100d16 0 25%,
      white 0 50%
    ) 0 / 18px 18px;
}

.wizard-qr > strong {
  color: #f08eff;
  font-size: 1.4rem;
}

.wizard-qr > small {
  margin-top: 5px;
  color: #9e92a8;
  font-family: ui-monospace, monospace;
}


/* Zahlung-erhalten-Button */

.payment-received-button {
  margin-top: 14px;
  padding: 10px 18px;

  border: 1px solid rgba(255,255,255,.12);
  border-radius: 9px;

  color: #d9cedf;
  background: rgba(255,255,255,.05);

  font-size: .72rem;
  font-weight: 800;

  opacity: 0;

  animation: payment-received-show 10s infinite;
}


/* Erfolgsfenster */

.purchase-success {
  position: absolute;

  padding: 18px 25px;

  display: flex;
  align-items: center;
  gap: 10px;

  border: 1px solid rgba(101,221,160,.35);
  border-radius: 13px;

  color: #dff8ea;
  background: rgba(42,110,74,.92);

  box-shadow: 0 18px 45px rgba(0,0,0,.35);

  opacity: 0;
  transform: scale(.85) translateY(10px);

  animation: purchase-success-show 10s infinite;
}

.purchase-success span {
  color: #65dda0;
  font-size: 1.4rem;
}


/* =========================================================
   HAUPTFENSTER
   ========================================================= */

@keyframes wizard-slide-one {
  0%, 38% {
    opacity: 1;
    transform: none;
  }

  40%, 100% {
    opacity: 0;
    transform: translateX(-22px);
  }
}

@keyframes wizard-slide-two {
  0%, 39% {
    opacity: 0;
    transform: translateX(25px);
  }

  41%, 64% {
    opacity: 1;
    transform: none;
  }

  66%, 100% {
    opacity: 0;
    transform: translateX(-22px);
  }
}

@keyframes wizard-slide-three {
  0%, 65% {
    opacity: 0;
    transform: translateX(25px);
  }

  67%, 100% {
    opacity: 1;
    transform: none;
  }
}


/* =========================================================
   FORTSCHRITTSBALKEN
   ========================================================= */

@keyframes wizard-step-one {
  0%, 39% {
    background: #e982f9;
  }

  40%, 100% {
    background: #44394d;
  }
}

@keyframes wizard-step-two {
  0%, 39% {
    background: #44394d;
  }

  40%, 65% {
    background: #e982f9;
  }

  66%, 100% {
    background: #44394d;
  }
}

@keyframes wizard-step-three {
  0%, 65% {
    background: #44394d;
  }

  66%, 100% {
    background: #e982f9;
  }
}


/* =========================================================
   FENSTER 1
   ========================================================= */

/* Vinyl: ungefähr Sekunde 2–4 */

@keyframes wizard-vinyl-highlight {
  0%, 12% {
    border-color: rgba(255,255,255,.1);
    background: rgba(255,255,255,.04);
  }

  14%, 25% {
    border-color: #e982f9;
    background: rgba(217,95,241,.18);
    color: white;
  }

  27%, 100% {
    border-color: rgba(255,255,255,.1);
    background: rgba(255,255,255,.04);
  }
}


/* Shirt: ungefähr Sekunde 4–6 */

@keyframes wizard-shirt-highlight {
  0%, 25% {
    border-color: rgba(255,255,255,.1);
    background: rgba(255,255,255,.04);
  }

  27%, 39% {
    border-color: #e982f9;
    background: rgba(217,95,241,.18);
    color: white;
  }

  40%, 100% {
    border-color: rgba(255,255,255,.1);
    background: rgba(255,255,255,.04);
  }
}


/* Warenkorb 0 Artikel */

@keyframes cart-state-zero {
  0%, 12% {
    opacity: 1;
  }

  14%, 100% {
    opacity: 0;
  }
}


/* Warenkorb 1 Artikel */

@keyframes cart-state-one {
  0%, 12% {
    opacity: 0;
  }

  14%, 25% {
    opacity: 1;
  }

  27%, 100% {
    opacity: 0;
  }
}


/* Warenkorb 2 Artikel */

@keyframes cart-state-two {
  0%, 25% {
    opacity: 0;
  }

  27%, 39% {
    opacity: 1;
  }

  40%, 100% {
    opacity: 0;
  }
}


/* =========================================================
   FENSTER 2
   ========================================================= */

@keyframes wizard-paypal-highlight {
  0%, 51% {
    border-color: rgba(255,255,255,.1);
    background: rgba(255,255,255,.04);
  }

  53%, 65% {
    border-color: #e982f9;
    background: rgba(217,95,241,.18);
    color: white;
  }

  66%, 100% {
    border-color: rgba(255,255,255,.1);
    background: rgba(255,255,255,.04);
  }
}


/* =========================================================
   FENSTER 3
   ========================================================= */

/* QR erscheint */

@keyframes wizard-qr-show {
  0%, 66% {
    opacity: 0;
    transform: scale(.92);
  }

  69%, 100% {
    opacity: 1;
    transform: scale(1);
  }
}


/* Button erscheint und wird pink */

@keyframes payment-received-show {
  0%, 78% {
    opacity: 0;
    border-color: rgba(255,255,255,.12);
    background: rgba(255,255,255,.05);
    color: #d9cedf;
  }

  80% {
    opacity: 1;
  }

  82%, 89% {
    opacity: 1;
    border-color: #e982f9;
    background: #e982f9;
    color: #260b2e;
  }

  91%, 100% {
    opacity: 1;
    border-color: rgba(255,255,255,.12);
    background: rgba(255,255,255,.05);
    color: #d9cedf;
  }
}


/* Erfolgsfenster am Ende */

@keyframes purchase-success-show {
  0%, 89% {
    opacity: 0;
    transform: scale(.85) translateY(10px);
  }

  92%, 98% {
    opacity: 1;
    transform: scale(1) translateY(0);
  }

  100% {
    opacity: 0;
    transform: scale(.95);
  }
}
.inventory-demo { padding: 25px; display: flex; flex-direction: column; justify-content: center; gap: 21px; }
.inventory-head { display: flex; justify-content: space-between; }.inventory-head span { color: #65dda0; font-size: .7rem; }
.stock-row { display: grid; grid-template-columns: 130px 1fr 28px; gap: 15px; align-items: center; color: #d9cfde; font-size: .76rem; }
.stock-row i { height: 7px; overflow: hidden; border-radius: 8px; background: #302737; }
.stock-row b { width: var(--stock); height: 100%; display: block; border-radius: 8px; background: linear-gradient(90deg,#b64ed0,#ef91ff); animation: stock-pulse 4s ease-in-out infinite alternate; }
.stock-row.warning b { background: #f3b35a; }.stock-row.warning strong { color: #f3b35a; }
.stock-event { margin-top: 7px; padding: 13px 15px; display: grid; grid-template-columns: auto 1fr auto; gap: 12px; border: 1px solid rgba(243,179,90,.2); border-radius: 10px; background: rgba(243,179,90,.08); font-size: .72rem; animation: event-pop 4s ease-in-out infinite; }
.stock-event > span { color:#f3b35a; font-weight: 850; }.stock-event small { color:#978c9f; }
@keyframes stock-pulse { to { width: calc(var(--stock) - 8%); } }
@keyframes event-pop { 0%,18% { opacity:0; transform:translateY(9px); } 28%,85% { opacity:1; transform:none; } 100% { opacity:0; } }

.mosaic-demo { padding: 13px; }
.mosaic-track { height: 314px; display: grid; grid-template-columns: 1.2fr .8fr 1fr; grid-template-rows: 1fr 1fr; gap: 8px; animation: mosaic-drift 8s ease-in-out infinite alternate; }
.merch-tile { --tile-x:-20px;--tile-y:0;position: relative; display: grid; place-items: center; overflow: hidden; border-radius: 10px; background: linear-gradient(145deg,#583264,#201426); animation:tile-reveal 8s ease-in-out infinite; }
.merch-tile:nth-child(2),.merch-tile:nth-child(5){--tile-x:0;--tile-y:-20px;animation-delay:.3s}.merch-tile:nth-child(3),.merch-tile:nth-child(6){--tile-x:20px;--tile-y:0;animation-delay:.6s}.merch-tile:nth-child(4){--tile-x:0;--tile-y:20px;animation-delay:.9s}
.merch-tile i { width: 52px; height: 52px; display:grid; place-items:center; border-radius:50%; background:rgba(255,255,255,.07); font-size:1.6rem; }
.merch-tile span { position:absolute; right:9px; bottom:8px; padding:4px 7px; border-radius:99px; color:#230c2a; background:#f1a0fc; font-size:.64rem; font-weight:850; }
.tile-record { background:linear-gradient(145deg,#272033,#70407b); }.tile-bag { background:linear-gradient(145deg,#713b63,#291823); }.tile-cap { background:linear-gradient(145deg,#332663,#181528); }.merch-tile.alt { filter:hue-rotate(38deg); }
@keyframes mosaic-drift { from { transform:scale(1.02) translate3d(-4px,5px,0); } to { transform:scale(1.06) translate3d(5px,-5px,0); } }
@keyframes tile-reveal { 0%,10%{opacity:.15;transform:translate(var(--tile-x),var(--tile-y))}24%,88%{opacity:1;transform:none}100%{opacity:.2} }

.feature-grid { display: grid; grid-template-columns: repeat(3,1fr); gap: 13px; }
.feature-card { min-height: 220px; padding: 25px; border: 1px solid var(--landing-line); border-radius: 16px; background: var(--landing-panel); opacity: 0; transform: translateY(18px); transition: opacity .5s, transform .5s, border-color .2s; }
.feature-card.is-visible { opacity: 1; transform: none; }.feature-card:hover { border-color: rgba(240,142,255,.35); }
.feature-icon { width: 39px; height: 39px; display:grid; place-items:center; border:1px solid rgba(240,142,255,.25); border-radius:11px; color:#ed93fb; background:rgba(217,95,241,.08); font-size:1.2rem; }
.feature-card h3 { margin: 30px 0 10px; font-size:1.08rem; }.feature-card p { margin:0; color:#a99daf; font-size:.87rem; line-height:1.6; }

.workflow-section { text-align: center; }.workflow-section .landing-heading { margin-right:auto; margin-left:auto; }
.workflow-grid { margin:0; padding:0; display:grid; grid-template-columns:repeat(3,1fr); gap:15px; list-style:none; text-align:left; }
.workflow-grid li { position:relative; min-height:230px; padding:26px; border-top:1px solid rgba(240,142,255,.35); background:linear-gradient(to bottom,rgba(217,95,241,.08),transparent 65%); opacity:0; transform:translateY(18px); transition:.55s; }
.workflow-grid li.is-visible { opacity:1; transform:none; }.workflow-grid li > span { color:#7e6688; font:700 .7rem ui-monospace,monospace; }.workflow-grid h3 { margin:40px 0 10px; font-size:1.2rem; }.workflow-grid p { color:#a99daf; font-size:.86rem; line-height:1.6; }

.registration-section { display:grid; grid-template-columns:.8fr 1.2fr; gap:clamp(40px,8vw,100px); align-items:start; }
.registration-intro { position:sticky; top:120px; }.registration-intro > p { max-width:430px; }
.registration-note { margin-top:30px; padding:17px; display:grid; gap:5px; border-left:2px solid #d95ff1; color:#9f94a8; background:rgba(217,95,241,.06); font-size:.82rem; line-height:1.5; }.registration-note strong { color:#eee7f1; }
.registration-card { min-height:550px; padding:clamp(22px,4vw,38px); border:1px solid rgba(255,255,255,.12); border-radius:20px; background:rgba(25,18,33,.88); box-shadow:0 35px 80px rgba(0,0,0,.3); }
.registration-form { display:grid; gap:17px; }.registration-form-heading { margin-bottom:6px; display:flex; gap:15px; align-items:start; }.registration-form-heading > span { color:#e782f7; font:700 .75rem ui-monospace,monospace; }.registration-form h3,.status-panel h3,.credential-panel h3,.registration-unavailable h3 { margin:0 0 7px; font-size:1.45rem; letter-spacing:-.035em; }.registration-form p,.status-panel > p,.credential-panel > p,.registration-unavailable p { margin:0; color:#a99daf; line-height:1.55; }
.registration-form label,.resume-link label { display:grid; gap:7px; color:#b9aFC0; font-size:.75rem; font-weight:730; }
.registration-form input,.resume-link input { width:100%; padding:11px 12px; border:1px solid rgba(255,255,255,.12); border-radius:9px; color:#f8f4fb; background:#0e0a13; outline:none; }.registration-form input:focus,.resume-link input:focus { border-color:#e982f9; box-shadow:0 0 0 3px rgba(217,95,241,.12); }
.registration-form small { color:#817687; font-size:.68rem; font-weight:500; }.registration-columns { display:grid; grid-template-columns:1fr 1fr; gap:12px; }.slug-field { display:grid; grid-template-columns:auto 1fr; align-items:center; overflow:hidden; border:1px solid rgba(255,255,255,.12); border-radius:9px; background:#0e0a13; }.slug-field > span { padding:0 0 0 12px; color:#7f7487; }.slug-field input { border:0; box-shadow:none; }.privacy-check { grid-template-columns:auto 1fr!important; align-items:start; line-height:1.45; cursor:pointer; }.privacy-check input { width:18px;height:18px;accent-color:#d95ff1; }.honeypot { position:absolute!important; left:-10000px!important; width:1px!important; height:1px!important; overflow:hidden!important; }.submit-registration { width:100%; margin-top:5px; }.landing-button:disabled { opacity:.55;cursor:wait; }
.landing-alert { margin-bottom:16px; padding:11px 13px; border-radius:9px; font-size:.8rem; line-height:1.45; }.landing-alert.error { color:#ffd8dc; border:1px solid rgba(242,121,131,.28); background:rgba(242,121,131,.1); }.landing-alert.success { color:#c9f4d9;border:1px solid rgba(82,209,139,.27);background:rgba(82,209,139,.09); }.landing-alert.warning { margin:16px 0 0;color:#ffe1b4;border:1px solid rgba(243,179,90,.25);background:rgba(243,179,90,.09); }
.status-panel,.credential-panel,.registration-unavailable { display:grid; justify-items:start; }.status-orb { width:48px;height:48px;margin-bottom:22px;display:grid;place-items:center;border:1px solid rgba(255,255,255,.16);border-radius:50%;color:#c9bcd0;background:rgba(255,255,255,.05);font-size:1.25rem;font-weight:850; }.status-orb.pending { animation:orb-pulse 1.8s infinite; }.status-orb.approved { color:#9ff0bc;border-color:rgba(82,209,139,.35);background:rgba(82,209,139,.1); }.status-orb.rejected,.status-orb.expired { color:#ffc4ca;border-color:rgba(242,121,131,.35);background:rgba(242,121,131,.1); }@keyframes orb-pulse{50%{box-shadow:0 0 0 9px rgba(217,95,241,.06)}}
.status-details,.credential-panel dl { width:100%;margin:25px 0 0;display:grid;gap:0;border-top:1px solid rgba(255,255,255,.09); }.status-details > div,.credential-panel dl > div { padding:11px 0;display:grid;grid-template-columns:minmax(110px,.7fr) 1fr;gap:15px;border-bottom:1px solid rgba(255,255,255,.07); }.status-details dt,.credential-panel dt { color:#8f8496;font-size:.72rem;font-weight:700; }.status-details dd,.credential-panel dd { margin:0;overflow-wrap:anywhere;font-size:.86rem; }.credential-panel code,.status-details code { color:#f09afe; }.credential-panel .setup-code dd code { display:inline-block;padding:8px 10px;border-radius:7px;color:#190820;background:#f1a0fc;font-size:1.05rem;font-weight:900;letter-spacing:.06em; }
.decision-note { width:100%;margin-top:18px!important;padding:13px;border-left:2px solid #f3b35a;background:rgba(243,179,90,.07); }.resume-link { width:100%;margin-top:19px;display:grid;grid-template-columns:1fr auto;gap:9px;align-items:end; }.resume-link .landing-button { min-height:42px; }.registration-loading { min-height:350px;display:grid;place-items:center;color:#a99daf; }

.faq-section { display:grid;grid-template-columns:.7fr 1.3fr;gap:60px; }.faq-list { border-top:1px solid rgba(255,255,255,.11); }.faq-list details { border-bottom:1px solid rgba(255,255,255,.11); }.faq-list summary { padding:20px 35px 20px 0;position:relative;cursor:pointer;font-weight:750;list-style:none; }.faq-list summary::after { position:absolute;right:4px;content:'+';color:#e88df7;font-size:1.2rem; }.faq-list details[open] summary::after { content:'−'; }.faq-list p { margin:0;padding:0 35px 20px 0;color:#a99daf;line-height:1.6; }
.landing-footer { padding:35px 0 48px;display:flex;align-items:center;gap:25px;border-top:1px solid rgba(255,255,255,.1);color:#8e8396;font-size:.78rem; }.landing-footer p { margin:auto; }.landing-footer > a:last-child { color:#e996f7;font-weight:750; }

.app-showcase:not(.is-visible) *, .demo-animated:not(.is-visible) *, .animations-paused * { animation-play-state: paused!important; }
@media (prefers-reduced-motion: reduce) { .landing-page * { scroll-behavior:auto!important; animation:none!important; transition:none!important; }.app-showcase,.story-row,.feature-card,.workflow-grid li { opacity:1;transform:none; } }
@media (max-width: 900px) { .landing-nav { display:none; }.hero-section,.registration-section { grid-template-columns:1fr; }.hero-section { padding-top:130px; }.hero-copy { text-align:center; }.hero-lead { margin-right:auto;margin-left:auto; }.hero-actions,.hero-trust { justify-content:center; }.hero-window { transform:none; }.story-row { grid-template-columns:1fr;gap:28px; }.story-reverse .story-copy { order:0; }.feature-grid { grid-template-columns:1fr 1fr; }.registration-intro { position:static; }.faq-section { grid-template-columns:1fr;gap:10px; } }
@media (max-width: 620px) { .landing-header { top:8px;right:8px;left:8px;min-height:56px;padding-left:11px;border-radius:14px; }.landing-brand > span:last-child { display:none; }.landing-actions { gap:6px; }.landing-button-small { min-height:36px;padding:7px 10px; }.landing-section,.landing-footer { width:min(100% - 28px,1180px); }.hero-section { min-height:auto;padding:125px 0 65px; }.hero-copy h1 { font-size:clamp(3rem,15vw,4.6rem); }.hero-app-grid { min-height:500px;grid-template-columns:1fr 1fr; }.demo-cart { grid-column:1/-1; }.demos-section,.feature-section,.workflow-section,.registration-section,.faq-section { padding:72px 0; }.story-row { min-height:0;margin-bottom:70px; }.wizard-demo,.inventory-demo,.mosaic-demo { min-height:300px; }.feature-grid,.workflow-grid,.registration-columns { grid-template-columns:1fr; }.feature-card { min-height:180px; }.workflow-grid li { min-height:190px; }.registration-card { padding:20px 16px; }.resume-link { grid-template-columns:1fr; }.status-details > div,.credential-panel dl > div { grid-template-columns:1fr;gap:4px; }.landing-footer { flex-wrap:wrap;justify-content:center;text-align:center; }.landing-footer p { width:100%;order:3; }.stock-row { grid-template-columns:105px 1fr 25px;gap:8px; }.mosaic-track { grid-template-columns:1fr 1fr; }.merch-tile:nth-child(n+5) { display:none; } }
</style>
