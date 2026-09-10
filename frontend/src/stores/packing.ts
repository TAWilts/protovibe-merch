import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import { packingApi } from '@/api/endpoints'
import type { Identity, PackingOperation, PackingOperationType, PackingSnapshot } from '@/api/types'
import {
  createPackingOperation,
  loadPackingSnapshot,
  packingPhotoBlob,
  packingFailures,
  packingQueueCount,
  queuePackingPhoto,
  rememberPackingContext,
  resumePackingSynchronization,
  resolvePackingFailure,
  savePackingSnapshot,
  synchronizePacking,
} from '@/offline/packing'
import type { PackingNamespace, PackingQueueRecord } from '@/offline/database'

export const usePackingStore = defineStore('packing', () => {
  const snapshot = ref<PackingSnapshot>({ revision: 0, generation: 1, bags: [] })
  const context = ref<PackingNamespace | null>(null)
  const loading = ref(false)
  const syncing = ref(false)
  const queued = ref(0)
  const messages = ref<string[]>([])
  const authenticationRequired = ref(false)
  const conflicts = ref<PackingQueueRecord[]>([])
  const photoURLs = ref<Record<string, string>>({})
  const online = ref(navigator.onLine)
  let timer: number | undefined
  let poller: number | undefined
  let events: EventSource | undefined
  let started = false

  const hasPending = computed(() => queued.value > 0)

  function identityContext(identity: Identity): PackingNamespace | null {
    if (!identity.band) return null
    return { bandId: identity.band.id, userId: identity.user.id }
  }

  async function refreshPhotoURLs() {
    if (!context.value) return
    const ids = snapshot.value.bags.flatMap((bag) => [
      ...bag.photos.map((photo) => photo.id),
      ...bag.items.flatMap((item) => item.photos.map((photo) => photo.id)),
    ])
    const next: Record<string, string> = {}
    for (const id of ids) {
      const blob = await packingPhotoBlob(context.value, id)
      if (blob) next[id] = URL.createObjectURL(blob)
      else if (online.value) next[id] = packingApi.photoUrl(id)
    }
    for (const [id, url] of Object.entries(photoURLs.value)) {
      if (url.startsWith('blob:') && next[id] !== url) URL.revokeObjectURL(url)
    }
    photoURLs.value = next
  }

  async function refreshQueue() {
    queued.value = context.value ? await packingQueueCount(context.value) : 0
    conflicts.value = context.value ? await packingFailures(context.value) : []
  }

  async function sync() {
    if (!context.value || syncing.value || !online.value) return
    syncing.value = true
    try {
      const result = await synchronizePacking(context.value)
      snapshot.value = result.snapshot
      authenticationRequired.value = result.authenticationRequired
      messages.value.push(...result.messages)
      await refreshQueue()
      await refreshPhotoURLs()
    } finally {
      syncing.value = false
    }
  }

  function scheduleSync(delay = 250) {
    if (timer !== undefined) window.clearTimeout(timer)
    timer = window.setTimeout(() => void sync(), delay)
  }

  function openEvents() {
    events?.close()
    if (!context.value || !online.value || typeof EventSource === 'undefined') return
    events = new EventSource('/api/v1/packing-list/events')
    events.addEventListener('revision', () => scheduleSync(0))
    events.onerror = () => {
      events?.close()
      events = undefined
      if (online.value) scheduleSync(5_000)
    }
  }

  async function prepare(identity: Identity) {
    const next = identityContext(identity)
    if (!next) return
    context.value = next
    resumePackingSynchronization(next)
    authenticationRequired.value = false
    await rememberPackingContext(next)
    loading.value = true
    try {
      const local = await loadPackingSnapshot(next)
      if (local) snapshot.value = local
      await refreshQueue()
      await refreshPhotoURLs()
      if (online.value) await sync()
    } finally {
      loading.value = false
    }
  }

  async function activate(identity: Identity) {
    await prepare(identity)
    if (context.value) {
      openEvents()
      if (poller !== undefined) window.clearInterval(poller)
      poller = window.setInterval(() => {
        if (document.visibilityState === 'visible') void sync()
      }, 10_000)
    }
  }

  async function mutate(type: PackingOperationType, payload: Partial<PackingOperation> = {}) {
    if (!context.value) return
    const result = await createPackingOperation(context.value, snapshot.value, type, payload)
    snapshot.value = result.snapshot
    await refreshQueue()
    scheduleSync()
  }

  async function addPhoto(input: { bagId?: string; itemId?: string; file: File }) {
    if (!context.value) return
    snapshot.value = await queuePackingPhoto(context.value, snapshot.value, input)
    await refreshQueue()
    await refreshPhotoURLs()
    scheduleSync()
  }

  function photoURL(id: string) { return photoURLs.value[id] ?? '' }
  function dismissMessage(index: number) { messages.value.splice(index, 1) }

  async function resolveConflict(key: string, retry: boolean) {
    await resolvePackingFailure(key, retry)
    await refreshQueue()
    await sync()
  }

  function start() {
    if (started) return
    started = true
    window.addEventListener('online', () => {
      online.value = true
      openEvents()
      scheduleSync(0)
    })
    window.addEventListener('offline', () => {
      online.value = false
      events?.close()
      events = undefined
    })
    window.addEventListener('focus', () => scheduleSync(0))
    document.addEventListener('visibilitychange', () => {
      if (document.visibilityState === 'visible') scheduleSync(0)
    })
  }

  function deactivatePage() {
    events?.close()
    events = undefined
    if (poller !== undefined) window.clearInterval(poller)
    poller = undefined
  }

  /** Drops user- and band-scoped in-memory data without deleting offline data. */
  function resetSession() {
    deactivatePage()
    if (timer !== undefined) window.clearTimeout(timer)
    timer = undefined
    for (const url of Object.values(photoURLs.value)) {
      if (url.startsWith('blob:')) URL.revokeObjectURL(url)
    }
    context.value = null
    snapshot.value = { revision: 0, generation: 1, bags: [] }
    loading.value = false
    syncing.value = false
    queued.value = 0
    messages.value = []
    authenticationRequired.value = false
    conflicts.value = []
    photoURLs.value = {}
  }

  return {
    snapshot, loading, syncing, queued, messages, conflicts, authenticationRequired, online, hasPending,
    prepare, activate, deactivatePage, resetSession, mutate, addPhoto, sync, start, photoURL, dismissMessage,
    resolveConflict,
    saveLocal: () => context.value && savePackingSnapshot(context.value, snapshot.value),
  }
})
