<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { platformApi } from '@/api/endpoints'
import { ApiError } from '@/api/client'
import type { PlatformSettings, UpdateStatus } from '@/api/types'
import type { TelemetryDaily } from '@/api/telemetry-types'
import { useFlashStore } from '@/stores/flash'
import { useSessionStore } from '@/stores/session'

/**
 * The instance-wide levers: maintenance mode, the announcement banner and
 * outgoing mail.
 *
 * Maintenance never blocks signing in, so an operator can always switch it
 * back off — that is enforced on the server, and stated here so nobody has to
 * find out by locking themselves out.
 */
const { t } = useI18n()
const flash = useFlashStore()
const session = useSessionStore()

const settings = ref<PlatformSettings | null>(null)
const loading = ref(true)
const busy = ref(false)
/** Write-only: an empty field leaves the stored password untouched. */
const smtpPassword = ref('')
const telemetryRows = ref<TelemetryDaily[]>([])

const canEdit = computed(() => session.capabilities?.is_system_admin ?? false)
const routeTelemetry = computed(() =>
  telemetryRows.value.filter((row) => row.event_kind === 'api_route'),
)
const requestCount = computed(() =>
  routeTelemetry.value.reduce((sum, row) => sum + row.sample_count, 0),
)
const averageRequestKb = computed(() => {
  const count = requestCount.value
  if (!count) return '0.0'
  return (
    routeTelemetry.value.reduce((sum, row) => sum + row.total_request_bytes, 0) /
    count /
    1024
  ).toFixed(1)
})
const averageResponseKb = computed(() => {
  const count = requestCount.value
  if (!count) return '0.0'
  return (
    routeTelemetry.value.reduce((sum, row) => sum + row.total_response_bytes, 0) /
    count /
    1024
  ).toFixed(1)
})
const roleRows = computed(() =>
  telemetryRows.value
    .filter((row) => row.event_kind === 'role')
    .reduce<Record<string, number>>((totals, row) => {
      totals[row.dimension] = (totals[row.dimension] ?? 0) + row.sample_count
      return totals
    }, {}),
)
const topPayment = computed(() => {
  const totals = telemetryRows.value
    .filter((row) => row.event_kind === 'payment_method')
    .reduce<Record<string, number>>((acc, row) => {
      acc[row.dimension] = (acc[row.dimension] ?? 0) + row.sample_count
      return acc
    }, {})
  return Object.entries(totals).sort((a, b) => b[1] - a[1])[0]?.[0] ?? '—'
})

onMounted(async () => {
  try {
    settings.value = await platformApi.settings()
    if (canEdit.value) {
      telemetryRows.value = (await platformApi.telemetry(30)).rows
    }
  } catch {
    flash.error(t('errors.generic'))
  } finally {
    loading.value = false
  }
})

/** The advisory release check and its answer. */
const release = ref<UpdateStatus | null>(null)
const checking = ref(false)

async function checkUpdates() {
  if (checking.value) return
  checking.value = true
  try {
    release.value = await platformApi.updates(true)
  } catch (error) {
    report(error)
  } finally {
    checking.value = false
  }
}

/** Sends one mail to the notification address, so a wrong setting shows up now. */
async function testMail() {
  if (busy.value || !settings.value) return
  busy.value = true
  try {
    const result = await platformApi.sendTestMail(settings.value.notification_email)
    flash.success(t('platform.settings.testMailSent', { to: result.to }))
  } catch (error) {
    report(error)
  } finally {
    busy.value = false
  }
}

function report(error: unknown) {
  flash.error(
    error instanceof ApiError
      ? t(`errors.${error.detailCode ?? 'generic'}`, error.message)
      : t('errors.network'),
  )
}

async function save(payload: Record<string, unknown>) {
  if (busy.value) return
  busy.value = true
  try {
    settings.value = await platformApi.saveSettings(payload)
    smtpPassword.value = ''
    flash.success(t('platform.settings.saved'))
  } catch (error) {
    flash.error(
      error instanceof ApiError
        ? t(`errors.${error.detailCode ?? 'generic'}`, error.message)
        : t('errors.network'),
    )
  } finally {
    busy.value = false
  }
}

function saveMaintenance() {
  if (!settings.value) return
  save({
    maintenance_enabled: settings.value.maintenance_enabled,
    maintenance_message: settings.value.maintenance_message,
  })
}

function saveAnnouncement() {
  if (!settings.value) return
  save({
    announcement_text: settings.value.announcement_text,
    announcement_level: settings.value.announcement_level,
  })
}

function saveSmtp() {
  if (!settings.value) return
  const payload: Record<string, unknown> = {
    smtp_enabled: settings.value.smtp_enabled,
    smtp_host: settings.value.smtp_host,
    smtp_port: settings.value.smtp_port,
    smtp_security: settings.value.smtp_security,
    smtp_username: settings.value.smtp_username,
    smtp_from: settings.value.smtp_from,
    notification_email: settings.value.notification_email,
  }
  if (smtpPassword.value) {
    payload.smtp_password = smtpPassword.value
  }
  save(payload)
}
</script>

<template>
  <main class="page-shell">
    <div class="page-title-row">
      <div>
        <p class="eyebrow">{{ t('platform.eyebrow') }}</p>
        <h1>{{ t('platform.settings.title') }}</h1>
      </div>
    </div>

    <p v-if="loading" class="muted">{{ t('common.loading') }}</p>
    <p v-else-if="!canEdit" class="muted">{{ t('platform.settings.readOnly') }}</p>

    <template v-else-if="settings">
      <section class="table-section">
        <div class="section-heading">
          <div>
            <h2>{{ t('platform.settings.maintenance') }}</h2>
            <p>{{ t('platform.settings.maintenanceHint') }}</p>
          </div>
        </div>
        <form class="stack-form" @submit.prevent="saveMaintenance">
          <label class="checkbox-row">
            <input v-model="settings.maintenance_enabled" type="checkbox" />
            <span>{{ t('platform.settings.maintenanceEnabled') }}</span>
          </label>
          <label>
            {{ t('platform.settings.maintenanceMessage') }}
            <input v-model="settings.maintenance_message" />
          </label>
          <button class="primary-button" type="submit" :disabled="busy">{{ t('common.save') }}</button>
        </form>
      </section>

      <section class="table-section">
        <div class="section-heading">
          <div>
            <h2>{{ t('platform.settings.announcement') }}</h2>
            <p>{{ t('platform.settings.announcementHint') }}</p>
          </div>
        </div>
        <form class="stack-form" @submit.prevent="saveAnnouncement">
          <label>
            {{ t('platform.settings.announcementText') }}
            <textarea v-model="settings.announcement_text" rows="2" />
          </label>
          <label>
            {{ t('platform.settings.announcementLevel') }}
            <select v-model="settings.announcement_level">
              <option value="info">{{ t('platform.settings.levels.info') }}</option>
              <option value="warning">{{ t('platform.settings.levels.warning') }}</option>
              <option value="critical">{{ t('platform.settings.levels.critical') }}</option>
            </select>
          </label>
          <button class="primary-button" type="submit" :disabled="busy">{{ t('common.save') }}</button>
        </form>
      </section>

      <section class="table-section">
        <div class="section-heading">
          <div>
            <h2>{{ t('platform.settings.smtp') }}</h2>
            <p>{{ t('platform.settings.smtpHint') }}</p>
          </div>
        </div>
        <form class="stack-form" @submit.prevent="saveSmtp">
          <label class="checkbox-row">
            <input v-model="settings.smtp_enabled" type="checkbox" />
            <span>{{ t('platform.settings.smtpEnabled') }}</span>
          </label>
          <div class="field-grid two-columns">
            <label>{{ t('platform.settings.smtpHost') }}<input v-model="settings.smtp_host" /></label>
            <label>{{ t('platform.settings.smtpPort') }}<input v-model.number="settings.smtp_port" type="number" /></label>
          </div>
          <div class="field-grid two-columns">
            <label>
              {{ t('platform.settings.smtpSecurity') }}
              <select v-model="settings.smtp_security">
                <option value="ssl">SSL (465)</option>
                <option value="starttls">STARTTLS (587)</option>
                <option value="none">{{ t('common.none') }}</option>
              </select>
            </label>
            <label>{{ t('platform.settings.smtpUser') }}<input v-model="settings.smtp_username" autocomplete="off" /></label>
          </div>
          <label>
            {{ t('platform.settings.smtpPassword') }}
            <input
              v-model="smtpPassword"
              type="password"
              autocomplete="new-password"
              :placeholder="settings.smtp_password_set ? t('platform.settings.passwordStored') : ''"
            />
          </label>
          <p class="muted">{{ t('platform.settings.passwordHint') }}</p>
          <div class="field-grid two-columns">
            <label>{{ t('platform.settings.smtpFrom') }}<input v-model="settings.smtp_from" /></label>
            <label>{{ t('platform.settings.notificationEmail') }}<input v-model="settings.notification_email" /></label>
          </div>
          <div class="form-actions">
            <button class="primary-button" type="submit" :disabled="busy">{{ t('common.save') }}</button>
            <!-- Without a test send, a wrong mailbox only shows up when a
                 support notification silently fails to arrive. -->
            <button
              class="secondary-button"
              type="button"
              :disabled="busy || !settings.smtp_enabled"
              @click="testMail"
            >{{ t('platform.settings.testMail') }}</button>
          </div>
          <p class="muted">{{ t('platform.settings.testMailHint') }}</p>
        </form>
      </section>

      <section class="table-section">
        <div class="section-heading">
          <div>
            <h2>{{ t('platform.settings.telemetry.title') }}</h2>
            <p>{{ t('platform.settings.telemetry.hint') }}</p>
          </div>
        </div>
        <div class="metric-grid">
          <article class="metric-card">
            <span>{{ t('platform.settings.telemetry.requests') }}</span>
            <strong>{{ requestCount }}</strong>
          </article>
          <article class="metric-card">
            <span>{{ t('platform.settings.telemetry.requestSize') }}</span>
            <strong>{{ averageRequestKb }} KiB</strong>
          </article>
          <article class="metric-card">
            <span>{{ t('platform.settings.telemetry.responseSize') }}</span>
            <strong>{{ averageResponseKb }} KiB</strong>
          </article>
          <article class="metric-card">
            <span>{{ t('platform.settings.telemetry.payment') }}</span>
            <strong>{{ topPayment }}</strong>
          </article>
        </div>
        <p class="muted">{{ t('platform.settings.telemetry.privacy') }}</p>
        <div v-if="Object.keys(roleRows).length" class="telemetry-role-list">
          <span v-for="(count, role) in roleRows" :key="role">
            <strong>{{ role }}</strong> · {{ count }}
          </span>
        </div>
      </section>

      <section class="table-section">
        <div class="section-heading">
          <div>
            <h2>{{ t('platform.settings.updates') }}</h2>
            <p>{{ t('platform.settings.updatesHint') }}</p>
          </div>
          <button class="secondary-button" type="button" :disabled="checking" @click="checkUpdates">
            {{ t('platform.settings.checkNow') }}
          </button>
        </div>

        <dl v-if="release" class="update-status">
          <div>
            <dt>{{ t('platform.settings.installed') }}</dt>
            <dd><code>{{ release.current }}</code></dd>
          </div>
          <div>
            <dt>{{ t('platform.settings.published') }}</dt>
            <dd>
              <code>{{ release.latest || '—' }}</code>
              <a v-if="release.url" :href="release.url" target="_blank" rel="noreferrer noopener">
                {{ t('platform.settings.releaseNotes') }}
              </a>
            </dd>
          </div>
        </dl>
        <p v-if="release?.newer_available" class="flash warning">
          {{ t('platform.settings.updateAvailable', { version: release.latest }) }}
        </p>
        <p v-else-if="release" class="muted">{{ t('platform.settings.upToDate') }}</p>
      </section>
    </template>
  </main>
</template>

<style scoped>
.form-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
}

.update-status {
  display: grid;
  gap: 8px;
  margin: 0 0 12px;
}

.update-status > div {
  display: grid;
  grid-template-columns: minmax(150px, 0.3fr) minmax(0, 1fr);
  gap: 12px;
  align-items: baseline;
}

.update-status dt {
  color: var(--muted);
  font-size: 0.78rem;
  font-weight: 700;
}

.update-status dd {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  margin: 0;
}

.telemetry-role-list {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 12px;
}

.telemetry-role-list span {
  padding: 6px 9px;
  border: 1px solid var(--border);
  border-radius: 999px;
}

.checkbox-row {
  display: flex;
  flex-direction: row;
  align-items: center;
  gap: 10px;
}

.checkbox-row input[type='checkbox'] {
  width: 1.05rem;
  height: 1.05rem;
  accent-color: var(--accent);
}
</style>
