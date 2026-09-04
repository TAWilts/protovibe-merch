<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'

import { platformApi } from '@/api/endpoints'
import type {
  AuditEntry,
  BackupRun,
  BandRegistrationRequest,
  BandSummary,
  PlatformSettings,
  SupportGrant,
  SupportMessage,
} from '@/api/types'
import { useFlashStore } from '@/stores/flash'
import { useSessionStore } from '@/stores/session'

const { d } = useI18n()
const flash = useFlashStore()
const session = useSessionStore()

const loading = ref(true)
const bands = ref<BandSummary[]>([])
const registrations = ref<BandRegistrationRequest[]>([])
const grants = ref<SupportGrant[]>([])
const messages = ref<SupportMessage[]>([])
const auditEntries = ref<AuditEntry[]>([])
const backups = ref<BackupRun[]>([])
const settings = ref<PlatformSettings | null>(null)

const isSystemAdmin = computed(() => session.capabilities?.is_system_admin ?? false)
const activeBands = computed(() => bands.value.filter((band) => band.is_active).length)
const totalUsers = computed(() => bands.value.reduce((sum, band) => sum + band.user_count, 0))
const totalStorage = computed(() => bands.value.reduce((sum, band) => sum + band.storage_bytes, 0))
const pendingRegistrations = computed(() => registrations.value.filter((entry) => entry.status === 'pending'))
const pendingGrants = computed(() => grants.value.filter((grant) => grant.status === 'pending' || grant.status === 'approved'))
const activeGrants = computed(() => grants.value.filter((grant) => grant.status === 'active'))
const attentionCount = computed(() => messages.value.length + pendingGrants.value.length + pendingRegistrations.value.length)

const recentGrants = computed(() => [...grants.value]
  .filter((grant) => ['pending', 'approved', 'active'].includes(grant.status))
  .sort((a, b) => b.created_at.localeCompare(a.created_at))
  .slice(0, 4))

const newestBands = computed(() => [...bands.value]
  .sort((a, b) => b.created_at.localeCompare(a.created_at))
  .slice(0, 5))

const latestBackup = computed(() => [...backups.value]
  .sort((a, b) => b.started_at.localeCompare(a.started_at))[0] ?? null)

const latestSuccessfulBackup = computed(() => [...backups.value]
  .filter((backup) => backup.status === 'succeeded')
  .sort((a, b) => b.started_at.localeCompare(a.started_at))[0] ?? null)

const announcementActive = computed(() => {
  if (!settings.value?.announcement_text) return false
  if (!settings.value.announcement_expires_at) return true
  return new Date(settings.value.announcement_expires_at).getTime() > Date.now()
})

onMounted(load)

async function load() {
  loading.value = true
  let failed = 0
  const safe = async (job: () => Promise<void>) => {
    try { await job() } catch { failed++ }
  }

  const jobs = [
    safe(async () => { bands.value = (await platformApi.bands()).bands }),
    safe(async () => { grants.value = (await platformApi.grants()).grants }),
    safe(async () => { messages.value = (await platformApi.messages(true)).messages }),
    safe(async () => { auditEntries.value = (await platformApi.audit({ limit: '6' })).entries }),
    safe(async () => { backups.value = (await platformApi.backups()).runs }),
    safe(async () => { settings.value = await platformApi.settings() }),
  ]

  if (isSystemAdmin.value) {
    jobs.push(safe(async () => {
      registrations.value = (await platformApi.registrationRequests('pending')).requests
    }))
  }

  await Promise.all(jobs)
  loading.value = false
  if (failed) flash.error(`Dashboard: ${failed} Datenquelle${failed === 1 ? '' : 'n'} konnte${failed === 1 ? '' : 'n'} nicht geladen werden.`)
}

function formatDate(value: string | null | undefined) {
  return value ? d(new Date(value), 'short') : '—'
}

function formatBytes(bytes: number) {
  if (bytes <= 0) return '0 B'
  const units = ['B', 'kB', 'MB', 'GB', 'TB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit++ }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`
}

function grantStatus(status: SupportGrant['status']) {
  return ({ pending: 'Wartet', approved: 'Freigegeben', denied: 'Abgelehnt', active: 'Aktiv', expired: 'Abgelaufen', revoked: 'Widerrufen' })[status]
}

function backupStatus(status: BackupRun['status']) {
  return ({ running: 'Läuft', succeeded: 'Erfolgreich', failed: 'Fehlgeschlagen' })[status]
}

function scopeLabel(scope: SupportGrant['scope']) {
  return scope === 'read_write' ? 'Lesen & Schreiben' : 'Nur lesen'
}
</script>

<template>
  <main class="page-shell dashboard-page">
    <div class="page-title-row">
      <div>
        <p class="eyebrow">Platform</p>
        <h1>Dashboard</h1>
        <p class="dashboard-intro">Die wichtigsten Vorgänge, Systemzustände und letzten Aktivitäten auf einen Blick.</p>
      </div>
      <button class="secondary-button" type="button" :disabled="loading" @click="load">
        {{ loading ? 'Lädt …' : 'Aktualisieren' }}
      </button>
    </div>

    <p v-if="loading" class="muted">Dashboard wird geladen …</p>

    <template v-else>
      <section v-if="settings?.maintenance_enabled || announcementActive" class="dashboard-alerts">
        <article v-if="settings?.maintenance_enabled" class="dashboard-alert critical">
          <div><strong>Wartungsmodus aktiv</strong><p>{{ settings.maintenance_message || 'Keine Wartungsmeldung hinterlegt.' }}</p></div>
          <RouterLink class="compact-button" :to="{ name: 'platform-settings' }">Einstellungen</RouterLink>
        </article>
        <article v-if="announcementActive" class="dashboard-alert" :class="settings?.announcement_level || 'info'">
          <div>
            <strong>Aktive Ankündigung</strong>
            <p>{{ settings?.announcement_text }}</p>
            <small v-if="settings?.announcement_expires_at">Bis {{ formatDate(settings.announcement_expires_at) }}</small>
          </div>
          <RouterLink class="compact-button" :to="{ name: 'platform-settings' }">Einstellungen</RouterLink>
        </article>
      </section>

      <section class="dashboard-metrics">
        <RouterLink class="dashboard-metric" :to="{ name: 'platform-bands' }">
          <span>Bands</span><strong>{{ bands.length }}</strong><small>{{ activeBands }} aktiv · {{ bands.length - activeBands }} inaktiv</small>
        </RouterLink>
        <RouterLink class="dashboard-metric" :to="{ name: 'platform-bands' }">
          <span>Bandkonten</span><strong>{{ totalUsers }}</strong><small>über alle Bands</small>
        </RouterLink>
        <RouterLink class="dashboard-metric" :to="{ name: 'platform-messages' }">
          <span>Offenes Postfach</span><strong>{{ messages.length }}</strong><small>ungelöste Nachrichten</small>
        </RouterLink>
        <RouterLink class="dashboard-metric" :to="{ name: 'platform-support' }">
          <span>Supportzugriffe</span><strong>{{ pendingGrants.length }}</strong><small>{{ activeGrants.length }} gerade aktiv</small>
        </RouterLink>
        <RouterLink v-if="isSystemAdmin" class="dashboard-metric" :class="{ warning: pendingRegistrations.length }" :to="{ name: 'platform-registrations' }">
          <span>Registrierungen</span><strong>{{ pendingRegistrations.length }}</strong><small>warten auf Prüfung</small>
        </RouterLink>
        <article v-else class="dashboard-metric muted-metric">
          <span>Registrierungen</span><strong>—</strong><small>nur für System-Admins</small>
        </article>
        <RouterLink class="dashboard-metric" :to="{ name: 'platform-bands' }">
          <span>Upload-Speicher</span><strong>{{ formatBytes(totalStorage) }}</strong><small>über alle Bands</small>
        </RouterLink>
      </section>

      <div class="dashboard-grid">
        <section class="table-section dashboard-card">
          <div class="dashboard-card-head">
            <div><p class="eyebrow">To-do</p><h2>Benötigt Aufmerksamkeit</h2></div>
            <span class="attention-count" :class="{ clear: attentionCount === 0 }">{{ attentionCount }}</span>
          </div>
          <div class="attention-list">
            <RouterLink class="attention-row" :to="{ name: 'platform-messages' }"><span><strong>Postfach</strong><small>Offene Fragen und Probleme</small></span><b>{{ messages.length }}</b></RouterLink>
            <RouterLink class="attention-row" :to="{ name: 'platform-support' }"><span><strong>Supportzugriffe</strong><small>Ausstehend oder freigegeben</small></span><b>{{ pendingGrants.length }}</b></RouterLink>
            <RouterLink v-if="isSystemAdmin" class="attention-row" :to="{ name: 'platform-registrations' }"><span><strong>Registrierungen</strong><small>Neue Bands prüfen</small></span><b>{{ pendingRegistrations.length }}</b></RouterLink>
          </div>
          <div v-if="messages.length" class="dashboard-preview">
            <h3>Neueste offene Nachrichten</h3>
            <RouterLink v-for="message in messages.slice(0, 3)" :key="message.id" class="preview-row" :to="{ name: 'platform-messages' }">
              <span><strong>{{ message.subject }}</strong><small>{{ message.band_name || `Band #${message.band_id}` }} · {{ message.sender_username }}</small></span>
              <time>{{ formatDate(message.created_at) }}</time>
            </RouterLink>
          </div>
        </section>

        <section class="table-section dashboard-card">
          <div class="dashboard-card-head"><div><p class="eyebrow">System</p><h2>Status</h2></div><RouterLink class="compact-button" :to="{ name: 'platform-settings' }">Einstellungen</RouterLink></div>
          <dl class="status-list">
            <div><dt>Wartungsmodus</dt><dd><span class="status-dot" :class="settings?.maintenance_enabled ? 'danger' : 'success'"></span>{{ settings?.maintenance_enabled ? 'Aktiv' : 'Aus' }}</dd></div>
            <div><dt>Ankündigung</dt><dd><span class="status-dot" :class="announcementActive ? 'warning' : 'success'"></span>{{ announcementActive ? 'Aktiv' : 'Keine' }}</dd></div>
            <div><dt>Letzter Backup-Lauf</dt><dd><span v-if="latestBackup" class="status-dot" :class="latestBackup.status === 'succeeded' ? 'success' : latestBackup.status === 'failed' ? 'danger' : 'warning'"></span>{{ latestBackup ? `${backupStatus(latestBackup.status)} · ${formatDate(latestBackup.started_at)}` : '—' }}</dd></div>
            <div><dt>Letzte erfolgreiche Sicherung</dt><dd>{{ latestSuccessfulBackup ? formatDate(latestSuccessfulBackup.finished_at || latestSuccessfulBackup.started_at) : '—' }}</dd></div>
          </dl>
          <RouterLink class="text-link" :to="{ name: 'platform-backups' }">Sicherungen öffnen →</RouterLink>
        </section>

        <section class="table-section dashboard-card">
          <div class="dashboard-card-head"><div><p class="eyebrow">Support</p><h2>Aktuelle Zugriffe</h2></div><RouterLink class="compact-button" :to="{ name: 'platform-support' }">Alle</RouterLink></div>
          <p v-if="!recentGrants.length" class="muted">Keine offenen oder aktiven Supportzugriffe.</p>
          <div v-else class="preview-list">
            <RouterLink v-for="grant in recentGrants" :key="grant.id" class="preview-row" :to="{ name: 'platform-support' }">
              <span><strong>Band #{{ grant.band_id }}</strong><small>{{ grant.requested_by_username }} · {{ scopeLabel(grant.scope) }}</small></span>
              <span class="status-pill" :class="grant.status">{{ grantStatus(grant.status) }}</span>
            </RouterLink>
          </div>
        </section>

        <section v-if="isSystemAdmin" class="table-section dashboard-card">
          <div class="dashboard-card-head"><div><p class="eyebrow">Onboarding</p><h2>Neue Registrierungen</h2></div><RouterLink class="compact-button" :to="{ name: 'platform-registrations' }">Alle</RouterLink></div>
          <p v-if="!pendingRegistrations.length" class="muted">Keine Registrierung wartet auf Prüfung.</p>
          <div v-else class="preview-list">
            <RouterLink v-for="request in pendingRegistrations.slice(0, 4)" :key="request.id" class="preview-row" :to="{ name: 'platform-registrations' }">
              <span><strong>{{ request.requested_band_name }}</strong><small>{{ request.requested_band_slug }} · {{ request.requested_contact_email }}</small></span>
              <time>{{ formatDate(request.created_at) }}</time>
            </RouterLink>
          </div>
        </section>

        <section class="table-section dashboard-card">
          <div class="dashboard-card-head"><div><p class="eyebrow">Audit</p><h2>Neueste Aktivitäten</h2></div><RouterLink class="compact-button" :to="{ name: 'platform-audit' }">Auditlog</RouterLink></div>
          <p v-if="!auditEntries.length" class="muted">Noch keine Audit-Einträge.</p>
          <div v-else class="preview-list">
            <RouterLink v-for="entry in auditEntries" :key="entry.id" class="preview-row" :to="{ name: 'platform-audit' }">
              <span><strong>{{ entry.action }}</strong><small>{{ entry.username || 'System' }}<template v-if="entry.band_name"> · {{ entry.band_name }}</template></small></span>
              <time>{{ formatDate(entry.created_at) }}</time>
            </RouterLink>
          </div>
        </section>

        <section class="table-section dashboard-card">
          <div class="dashboard-card-head"><div><p class="eyebrow">Tenants</p><h2>Neueste Bands</h2></div><RouterLink class="compact-button" :to="{ name: 'platform-bands' }">Alle</RouterLink></div>
          <p v-if="!newestBands.length" class="muted">Noch keine Bands vorhanden.</p>
          <div v-else class="preview-list">
            <RouterLink v-for="band in newestBands" :key="band.id" class="preview-row" :to="{ name: 'platform-bands' }">
              <span><strong>{{ band.name }}</strong><small>{{ band.slug }} · {{ band.user_count }} Konten</small></span>
              <time>{{ formatDate(band.created_at) }}</time>
            </RouterLink>
          </div>
        </section>
      </div>
    </template>
  </main>
</template>

<style scoped>
.dashboard-page { display: grid; gap: 18px; }
.dashboard-intro { max-width: 760px; margin: 6px 0 0; color: var(--muted); }
.dashboard-alerts { display: grid; gap: 10px; }
.dashboard-alert { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 14px 16px; border: 1px solid var(--warning); border-radius: var(--radius); background: color-mix(in srgb, var(--warning) 10%, var(--panel)); }
.dashboard-alert.critical { border-color: var(--danger); background: color-mix(in srgb, var(--danger) 10%, var(--panel)); }
.dashboard-alert p { margin: 4px 0 0; }
.dashboard-alert small { color: var(--muted); }
.dashboard-metrics { display: grid; grid-template-columns: repeat(6, minmax(130px, 1fr)); gap: 12px; }
.dashboard-metric { display: grid; gap: 5px; min-width: 0; padding: 15px; border: 1px solid var(--border); border-radius: var(--radius); color: var(--text); background: var(--panel); text-decoration: none; }
.dashboard-metric:hover { border-color: var(--accent); background: var(--panel-raised); }
.dashboard-metric.warning { border-color: var(--warning); }
.dashboard-metric > span, .dashboard-metric small { color: var(--muted); }
.dashboard-metric strong { font-size: clamp(1.45rem, 2vw, 2rem); line-height: 1; }
.muted-metric { opacity: 0.72; }
.dashboard-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; align-items: start; }
.dashboard-card { min-width: 0; }
.dashboard-card-head { display: flex; align-items: center; justify-content: space-between; gap: 14px; margin-bottom: 14px; }
.dashboard-card-head h2, .dashboard-card-head p { margin: 0; }
.attention-count { display: grid; min-width: 38px; height: 38px; place-items: center; padding: 0 10px; border-radius: 999px; color: var(--panel); background: var(--warning); font-size: 1.1rem; font-weight: 850; }
.attention-count.clear { background: var(--success); }
.attention-list, .preview-list { display: grid; gap: 7px; }
.attention-row, .preview-row { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-width: 0; padding: 10px 11px; border-radius: 10px; color: var(--text); background: var(--panel-raised); text-decoration: none; }
.attention-row:hover, .preview-row:hover { background: var(--selection-hover); }
.attention-row > span, .preview-row > span:first-child { min-width: 0; }
.attention-row small, .preview-row small { display: block; margin-top: 2px; overflow: hidden; color: var(--muted); text-overflow: ellipsis; white-space: nowrap; }
.dashboard-preview { margin-top: 16px; padding-top: 14px; border-top: 1px solid var(--border); }
.dashboard-preview h3 { margin: 0 0 8px; font-size: .9rem; }
.preview-row time { flex: 0 0 auto; color: var(--muted); font-size: .78rem; white-space: nowrap; }
.status-list { display: grid; margin: 0; }
.status-list > div { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 10px 0; border-bottom: 1px solid var(--border); }
.status-list dt { color: var(--muted); }
.status-list dd { display: flex; align-items: center; gap: 7px; margin: 0; text-align: right; }
.status-dot { width: 8px; height: 8px; flex: 0 0 auto; border-radius: 50%; background: var(--muted); }
.status-dot.success { background: var(--success); }.status-dot.warning { background: var(--warning); }.status-dot.danger { background: var(--danger); }
.status-pill { flex: 0 0 auto; padding: 3px 8px; border-radius: 999px; color: var(--muted); background: var(--option-bg); font-size: .74rem; font-weight: 700; }
.status-pill.active { color: var(--success); }.status-pill.pending, .status-pill.approved { color: var(--warning); }
.text-link { display: inline-block; margin-top: 13px; color: var(--accent-bright); text-decoration: none; font-weight: 650; }
@media (max-width: 1200px) { .dashboard-metrics { grid-template-columns: repeat(3, minmax(140px, 1fr)); } }
@media (max-width: 800px) { .dashboard-grid { grid-template-columns: 1fr; }.dashboard-metrics { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
@media (max-width: 520px) { .dashboard-metrics { grid-template-columns: 1fr; }.dashboard-alert, .dashboard-card-head, .status-list > div { align-items: flex-start; flex-direction: column; }.status-list dd { text-align: left; } }
</style>
