<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'

import type { PackingBag, PackingItem, PackingPhoto, PackingStatus } from '@/api/types'
import AppDialog from '@/components/ui/AppDialog.vue'
import { useFlashStore } from '@/stores/flash'
import { usePackingStore } from '@/stores/packing'
import { useSessionStore } from '@/stores/session'

const { t } = useI18n()
const packing = usePackingStore()
const session = useSessionStore()
const flash = useFlashStore()
const router = useRouter()

const canManage = computed(() => (
  (session.capabilities?.can_manage_packing_list ?? false) && !session.posMode
))
const collapsed = ref<Set<string>>(new Set())
const editor = ref<{
  kind: 'create_bag' | 'rename_bag' | 'create_item' | 'rename_item'
  bagId?: string
  itemId?: string
  name: string
} | null>(null)
const confirming = ref<{ type: 'bag' | 'item' | 'photo' | 'conflict' | 'reset'; id?: string; name?: string } | null>(null)
const actionTarget = ref<{ kind: 'bag' | 'item'; bagId: string; itemId?: string } | null>(null)
const gallery = ref<{ bagId?: string; itemId?: string; name: string } | null>(null)
const draggedBag = ref('')
const draggedItem = ref<{ bagId: string; itemId: string } | null>(null)

let holdTimer: number | undefined
let holdStart: { x: number; y: number } | null = null
let suppressNextClick = false

const collapseKey = computed(() => `protovibe.packing.collapsed.v1:${session.band?.id ?? 0}`)
const totals = computed(() => {
  let packed = 0
  let open = 0
  let stays = 0
  for (const bag of packing.snapshot.bags) {
    if (bag.status === 'stays_here') {
      stays += Math.max(1, bag.items.length)
      continue
    }
    if (!bag.items.length) {
      if (bag.status === 'packed') packed++
      else open++
      continue
    }
    for (const item of bag.items) {
      if (item.status === 'stays_here') stays++
      else if (item.status === 'packed') packed++
      else open++
    }
  }
  return { packed, open, stays, active: packed + open }
})
const totalProgress = computed(() => totals.value.active ? (totals.value.packed / totals.value.active) * 100 : 0)
const galleryPhotos = computed<PackingPhoto[]>(() => {
  if (!gallery.value) return []
  if (gallery.value.itemId) {
    return packing.snapshot.bags.flatMap((bag) => bag.items)
      .find((item) => item.id === gallery.value!.itemId)?.photos ?? []
  }
  return packing.snapshot.bags.find((bag) => bag.id === gallery.value?.bagId)?.photos ?? []
})

onMounted(async () => {
  loadCollapsed()
  if (session.identity) await packing.activate(session.identity)
})
onUnmounted(() => {
  cancelHold()
  packing.deactivatePage()
})
watch(collapseKey, loadCollapsed)

function loadCollapsed() {
  try {
    const stored = JSON.parse(localStorage.getItem(collapseKey.value) ?? '[]')
    collapsed.value = new Set(Array.isArray(stored) ? stored : [])
  } catch {
    collapsed.value = new Set()
  }
}

function toggleCollapsed(id: string) {
  const next = new Set(collapsed.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  collapsed.value = next
  localStorage.setItem(collapseKey.value, JSON.stringify([...next]))
}

function reauthenticate() {
  session.adopt(null)
  void router.push({ name: 'login', query: { next: '/packing-list' } })
}

function bagCounts(bag: PackingBag) {
  if (bag.status === 'stays_here') return { packed: 0, open: 0, stays: Math.max(1, bag.items.length), active: 0 }
  if (!bag.items.length) {
    const packed = bag.status === 'packed' ? 1 : 0
    return { packed, open: packed ? 0 : 1, stays: 0, active: 1 }
  }
  let packed = 0
  let open = 0
  let stays = 0
  for (const item of bag.items) {
    if (item.status === 'stays_here') stays++
    else if (item.status === 'packed') packed++
    else open++
  }
  return { packed, open, stays, active: packed + open }
}

function bagProgress(bag: PackingBag) {
  const counts = bagCounts(bag)
  return counts.active ? (counts.packed / counts.active) * 100 : 0
}

function bagStateLabel(bag: PackingBag) {
  if (bag.status === 'stays_here') return t('packing.staysHere')
  const counts = bagCounts(bag)
  if (!counts.open && counts.active) return t('packing.complete')
  return t('packing.bagProgress', { packed: counts.packed, total: counts.active })
}

function itemStateLabel(item: PackingItem) {
  return item.status === 'packed'
    ? t('packing.packed')
    : item.status === 'stays_here' ? t('packing.staysHere') : t('packing.open')
}

async function mutate(type: Parameters<typeof packing.mutate>[0], payload = {}) {
  try {
    await packing.mutate(type, payload)
  } catch {
    flash.error(t('packing.localError'))
  }
}

function toggleBag(bag: PackingBag) {
  const counts = bagCounts(bag)
  const status: PackingStatus = counts.active > 0 && counts.open === 0 ? 'open' : 'packed'
  void mutate('set_bag_status', { bag_id: bag.id, status })
}

function toggleBagStays(bag: PackingBag) {
  const status: PackingStatus = bag.status === 'stays_here' ? 'open' : 'stays_here'
  void mutate('set_bag_status', { bag_id: bag.id, status })
}

function toggleItem(item: PackingItem) {
  const status: PackingStatus = item.status === 'packed' ? 'open' : 'packed'
  void mutate('set_item_status', { item_id: item.id, status })
}

function toggleItemStays(item: PackingItem) {
  const status: PackingStatus = item.status === 'stays_here' ? 'open' : 'stays_here'
  void mutate('set_item_status', { item_id: item.id, status })
}

function openEditor(kind: NonNullable<typeof editor.value>['kind'], bag?: PackingBag, item?: PackingItem) {
  editor.value = {
    kind,
    bagId: bag?.id,
    itemId: item?.id,
    name: item?.name ?? (kind === 'rename_bag' ? bag?.name ?? '' : ''),
  }
}

async function saveEditor() {
  if (!editor.value) return
  const name = editor.value.name.trim()
  if (!name) return
  const entry = editor.value
  if (entry.kind === 'create_bag') await mutate('create_bag', { bag_id: crypto.randomUUID(), name })
  else if (entry.kind === 'rename_bag') await mutate('rename_bag', { bag_id: entry.bagId, name })
  else if (entry.kind === 'create_item') await mutate('create_item', { bag_id: entry.bagId, item_id: crypto.randomUUID(), name })
  else await mutate('rename_item', { item_id: entry.itemId, name })
  editor.value = null
}

async function confirmAction() {
  const action = confirming.value
  if (!action) return
  if (action.type === 'bag') await mutate('delete_bag', { bag_id: action.id })
  else if (action.type === 'item') await mutate('delete_item', { item_id: action.id })
  else if (action.type === 'photo') await mutate('delete_photo', { photo_id: action.id })
  else if (action.type === 'conflict') await packing.resolveConflict(action.id!, false)
  else await mutate('reset')
  confirming.value = null
}

async function addPhotos(event: Event, owner: { bagId?: string; itemId?: string }) {
  const input = event.target as HTMLInputElement
  const files = Array.from(input.files ?? [])
  input.value = ''
  for (const file of files) {
    try {
      await packing.addPhoto({ ...owner, file })
    } catch {
      flash.error(t('packing.photoError', { name: file.name }))
    }
  }
}

async function addTargetPhotos(event: Event) {
  const target = actionTarget.value
  if (!target) return
  await addPhotos(event, target.kind === 'bag' ? { bagId: target.bagId } : { itemId: target.itemId })
  actionTarget.value = null
}

function moveBag(id: string, delta: number) {
  const order = packing.snapshot.bags.map((bag) => bag.id)
  const from = order.indexOf(id)
  const to = from + delta
  if (from < 0 || to < 0 || to >= order.length) return
  ;[order[from], order[to]] = [order[to]!, order[from]!]
  void mutate('reorder_bags', { order_ids: order })
}

function moveItem(bag: PackingBag, id: string, delta: number) {
  const order = bag.items.map((item) => item.id)
  const from = order.indexOf(id)
  const to = from + delta
  if (from < 0 || to < 0 || to >= order.length) return
  ;[order[from], order[to]] = [order[to]!, order[from]!]
  void mutate('reorder_items', { bag_id: bag.id, order_ids: order })
}

function dropBag(targetId: string) {
  cancelHold()
  const sourceId = draggedBag.value
  draggedBag.value = ''
  if (!sourceId || sourceId === targetId) return
  const order = packing.snapshot.bags.map((bag) => bag.id).filter((id) => id !== sourceId)
  order.splice(order.indexOf(targetId), 0, sourceId)
  void mutate('reorder_bags', { order_ids: order })
}

function dropItem(bag: PackingBag, targetId: string) {
  cancelHold()
  const source = draggedItem.value
  draggedItem.value = null
  if (!source || source.bagId !== bag.id || source.itemId === targetId) return
  const order = bag.items.map((item) => item.id).filter((id) => id !== source.itemId)
  order.splice(order.indexOf(targetId), 0, source.itemId)
  void mutate('reorder_items', { bag_id: bag.id, order_ids: order })
}

function beginHold(event: PointerEvent, target: NonNullable<typeof actionTarget.value>) {
  if (event.button !== 0) return
  cancelHold()
  holdStart = { x: event.clientX, y: event.clientY }
  holdTimer = window.setTimeout(() => {
    suppressNextClick = true
    actionTarget.value = target
    holdTimer = undefined
  }, 3_000)
}

function moveHold(event: PointerEvent) {
  if (!holdStart || holdTimer === undefined) return
  if (Math.hypot(event.clientX - holdStart.x, event.clientY - holdStart.y) > 10) cancelHold()
}

function cancelHold() {
  if (holdTimer !== undefined) window.clearTimeout(holdTimer)
  holdTimer = undefined
  holdStart = null
}

function clickAfterHold(action: () => void, event: MouseEvent) {
  cancelHold()
  if (suppressNextClick) {
    suppressNextClick = false
    event.preventDefault()
    return
  }
  action()
}

function currentAction() {
  const target = actionTarget.value
  if (!target) return { bag: undefined, item: undefined }
  const bag = packing.snapshot.bags.find((entry) => entry.id === target.bagId)
  const item = target.itemId ? bag?.items.find((entry) => entry.id === target.itemId) : undefined
  return { bag, item }
}

function toggleActionStays() {
  const { bag, item } = currentAction()
  if (item) toggleItemStays(item)
  else if (bag) toggleBagStays(bag)
  actionTarget.value = null
}

function renameAction() {
  const { bag, item } = currentAction()
  if (item && bag) openEditor('rename_item', bag, item)
  else if (bag) openEditor('rename_bag', bag)
  actionTarget.value = null
}

function moveAction(delta: number) {
  const { bag, item } = currentAction()
  if (item && bag) moveItem(bag, item.id, delta)
  else if (bag) moveBag(bag.id, delta)
  actionTarget.value = null
}

function deleteAction() {
  const { bag, item } = currentAction()
  if (item) confirming.value = { type: 'item', id: item.id, name: item.name }
  else if (bag) confirming.value = { type: 'bag', id: bag.id, name: bag.name }
  actionTarget.value = null
}
</script>

<template>
  <main class="packing-page page-shell">
    <header class="packing-heading">
      <div>
        <p class="eyebrow">{{ t('packing.eyebrow') }}</p>
        <h1>{{ t('packing.title') }}</h1>
        <p>{{ t('packing.intro') }}</p>
      </div>
    </header>

    <section class="packing-overview" :aria-label="t('packing.progress')">
      <div class="overview-copy">
        <strong>{{ t('packing.progress') }}</strong>
        <span>{{ t('packing.overallProgress', { packed: totals.packed, total: totals.active }) }}</span>
        <small v-if="totals.stays">{{ totals.stays }} {{ t('packing.staysShort') }}</small>
      </div>
      <div class="progress-track" role="progressbar" :aria-label="t('packing.progress')" :aria-valuenow="Math.round(totalProgress)" aria-valuemin="0" aria-valuemax="100">
        <span :style="{ width: `${totalProgress}%` }"></span>
      </div>
      <button v-if="canManage" class="secondary-button" type="button" @click="confirming = { type: 'reset' }">{{ t('packing.reset') }}</button>
    </section>

    <div v-for="(message, index) in packing.messages" :key="`${message}-${index}`" class="packing-message" role="status">
      <span>{{ message }}</span>
      <button type="button" @click="packing.dismissMessage(index)">{{ t('common.close') }}</button>
    </div>
    <div v-if="packing.authenticationRequired" class="packing-message" role="alert">
      <span>{{ t('packing.authenticationRequired') }}</span>
      <button type="button" @click="reauthenticate">{{ t('packing.signInAgain') }}</button>
    </div>
    <section v-if="packing.conflicts.length" class="packing-conflicts" aria-live="polite">
      <h2>{{ t('packing.conflictsTitle') }}</h2>
      <p>{{ t('packing.conflictsIntro') }}</p>
      <article v-for="conflict in packing.conflicts" :key="conflict.key">
        <span>{{ conflict.operation?.name || conflict.photo?.filename || conflict.operation?.type || t('packing.unknownChange') }}</span>
        <small>{{ conflict.lastError }}</small>
        <div class="row-actions">
          <button type="button" :disabled="!packing.online" @click="packing.resolveConflict(conflict.key, true)">{{ t('packing.retry') }}</button>
          <button type="button" @click="confirming = { type: 'conflict', id: conflict.key, name: conflict.operation?.name || conflict.photo?.filename }">{{ t('packing.discard') }}</button>
        </div>
      </article>
    </section>

    <p v-if="packing.loading && !packing.snapshot.bags.length" class="empty-state">{{ t('common.loading') }}</p>
    <p v-else-if="!packing.snapshot.bags.length" class="empty-state">{{ t('packing.empty') }}</p>

    <section class="bag-list">
      <article v-for="bag in packing.snapshot.bags" :key="bag.id" class="packing-bag" :class="{ excluded: bag.status === 'stays_here' }" :draggable="canManage" @dragstart="cancelHold(); draggedBag = bag.id" @dragover.prevent @drop.prevent="dropBag(bag.id)">
        <header class="bag-row">
          <button class="bag-progress-button" :class="{ excluded: bag.status === 'stays_here' }" :style="{ '--bag-progress': `${bagProgress(bag)}%` }" type="button" :aria-label="`${bag.name}: ${bagStateLabel(bag)}`" :aria-pressed="bagCounts(bag).active > 0 && bagCounts(bag).open === 0" @pointerdown="beginHold($event, { kind: 'bag', bagId: bag.id })" @pointermove="moveHold" @pointerup="cancelHold" @pointercancel="cancelHold" @pointerleave="cancelHold" @contextmenu.prevent @click="clickAfterHold(() => toggleBag(bag), $event)">
            <span class="bag-name">{{ bag.name }}</span><small>{{ bagStateLabel(bag) }}</small>
          </button>

          <button v-if="bag.photos.length" class="photo-trigger has-photo" type="button" :aria-label="t('packing.showPhotos', { name: bag.name })" @click="gallery = { bagId: bag.id, name: bag.name }">
            <img v-if="packing.photoURL(bag.photos[0]!.id)" :src="packing.photoURL(bag.photos[0]!.id)" :alt="bag.photos[0]!.original_filename" /><span v-if="bag.photos.length > 1">+{{ bag.photos.length - 1 }}</span>
          </button>
          <label v-else-if="canManage" class="photo-trigger" :aria-label="t('packing.addBagPhotos')"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M8.5 5 10 3h4l1.5 2H19a2 2 0 0 1 2 2v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7a2 2 0 0 1 2-2h3.5ZM12 8a4.5 4.5 0 1 0 0 9 4.5 4.5 0 0 0 0-9Zm0 2a2.5 2.5 0 1 1 0 5 2.5 2.5 0 0 1 0-5Z" /></svg><input type="file" accept="image/jpeg,image/png,image/webp" multiple @change="addPhotos($event, { bagId: bag.id })" /></label>
          <span v-else class="photo-trigger disabled" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M8.5 5 10 3h4l1.5 2H19a2 2 0 0 1 2 2v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7a2 2 0 0 1 2-2h3.5ZM12 8a4.5 4.5 0 1 0 0 9 4.5 4.5 0 0 0 0-9Zm0 2a2.5 2.5 0 1 1 0 5 2.5 2.5 0 0 1 0-5Z" /></svg></span>
          <button class="more-button" type="button" :aria-label="t('packing.actionsFor', { name: bag.name })" @click="actionTarget = { kind: 'bag', bagId: bag.id }">•••</button>
          <button class="collapse-button" type="button" :aria-expanded="!collapsed.has(bag.id)" :aria-label="t(collapsed.has(bag.id) ? 'packing.expand' : 'packing.collapse', { name: bag.name })" @click="toggleCollapsed(bag.id)"><svg viewBox="0 0 24 24" aria-hidden="true" :class="{ collapsed: collapsed.has(bag.id) }"><path d="m6 9 6 6 6-6" /></svg></button>
        </header>

        <div v-if="!collapsed.has(bag.id)" class="bag-body">
          <div class="item-list" :class="{ muted: bag.status === 'stays_here' }">
            <article v-for="item in bag.items" :key="item.id" class="item-row" :class="`state-${item.status}`" :draggable="canManage" @dragstart.stop="cancelHold(); draggedItem = { bagId: bag.id, itemId: item.id }" @dragover.prevent @drop.prevent.stop="dropItem(bag, item.id)">
              <button class="item-toggle" type="button" :disabled="bag.status === 'stays_here'" :aria-label="`${item.name}: ${itemStateLabel(item)}`" :aria-pressed="item.status === 'packed'" @pointerdown="beginHold($event, { kind: 'item', bagId: bag.id, itemId: item.id })" @pointermove="moveHold" @pointerup="cancelHold" @pointercancel="cancelHold" @pointerleave="cancelHold" @contextmenu.prevent @click="clickAfterHold(() => toggleItem(item), $event)">
                <span aria-hidden="true" class="state-icon">{{ item.status === 'packed' ? '✓' : item.status === 'stays_here' ? '—' : '○' }}</span><span>{{ item.name }}</span><small>{{ itemStateLabel(item) }}</small>
              </button>
              <button v-if="item.photos.length" class="photo-trigger has-photo" type="button" :aria-label="t('packing.showPhotos', { name: item.name })" @click="gallery = { itemId: item.id, name: item.name }">
                <img v-if="packing.photoURL(item.photos[0]!.id)" :src="packing.photoURL(item.photos[0]!.id)" :alt="item.photos[0]!.original_filename" /><span v-if="item.photos.length > 1">+{{ item.photos.length - 1 }}</span>
              </button>
              <label v-else-if="canManage" class="photo-trigger" :aria-label="t('packing.addItemPhotos')"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M8.5 5 10 3h4l1.5 2H19a2 2 0 0 1 2 2v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7a2 2 0 0 1 2-2h3.5ZM12 8a4.5 4.5 0 1 0 0 9 4.5 4.5 0 0 0 0-9Zm0 2a2.5 2.5 0 1 1 0 5 2.5 2.5 0 0 1 0-5Z" /></svg><input type="file" accept="image/jpeg,image/png,image/webp" multiple @change="addPhotos($event, { itemId: item.id })" /></label>
              <span v-else class="photo-trigger disabled" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M8.5 5 10 3h4l1.5 2H19a2 2 0 0 1 2 2v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7a2 2 0 0 1 2-2h3.5ZM12 8a4.5 4.5 0 1 0 0 9 4.5 4.5 0 0 0 0-9Zm0 2a2.5 2.5 0 1 1 0 5 2.5 2.5 0 0 1 0-5Z" /></svg></span>
              <button class="more-button" type="button" :aria-label="t('packing.actionsFor', { name: item.name })" @click="actionTarget = { kind: 'item', bagId: bag.id, itemId: item.id }">•••</button>
            </article>
          </div>
          <button v-if="canManage" class="add-row-button" type="button" :aria-label="t('packing.addItem')" @click="openEditor('create_item', bag)"><span aria-hidden="true">+</span><span>{{ t('packing.addItem') }}</span></button>
        </div>
      </article>
    </section>

    <button v-if="canManage" class="add-row-button add-bag-button" type="button" @click="openEditor('create_bag')"><span aria-hidden="true">+</span><span>{{ t('packing.addBag') }}</span></button>

    <AppDialog v-if="actionTarget" :label="t('packing.actionsTitle')" @close="actionTarget = null">
      <div class="stack-form packing-action-menu">
        <h2>{{ currentAction().item?.name ?? currentAction().bag?.name }}</h2>
        <button class="secondary-button" type="button" @click="toggleActionStays">{{ (currentAction().item?.status ?? currentAction().bag?.status) === 'stays_here' ? t('packing.takeAlong') : t('packing.staysHere') }}</button>
        <template v-if="canManage">
          <button class="secondary-button" type="button" @click="renameAction">{{ t('packing.rename') }}</button>
          <label class="secondary-button action-upload">{{ t('packing.addOrChangePhoto') }}<input type="file" accept="image/jpeg,image/png,image/webp" multiple @change="addTargetPhotos" /></label>
          <div class="action-move-row"><button class="secondary-button" type="button" @click="moveAction(-1)">↑ {{ t('packing.moveUp') }}</button><button class="secondary-button" type="button" @click="moveAction(1)">↓ {{ t('packing.moveDown') }}</button></div>
          <button class="danger-button" type="button" @click="deleteAction">{{ t('common.delete') }}</button>
        </template>
      </div>
    </AppDialog>

    <AppDialog v-if="gallery" :label="t('packing.photosFor', { name: gallery.name })" @close="gallery = null">
      <div class="stack-form packing-gallery">
        <h2>{{ t('packing.photosFor', { name: gallery.name }) }}</h2>
        <div class="photo-grid">
          <figure v-for="photo in galleryPhotos" :key="photo.id"><img v-if="packing.photoURL(photo.id)" :src="packing.photoURL(photo.id)" :alt="photo.original_filename" /><figcaption>{{ photo.original_filename }}</figcaption><button v-if="canManage" type="button" :aria-label="t('packing.deletePhoto', { name: photo.original_filename })" @click="confirming = { type: 'photo', id: photo.id, name: photo.original_filename }">×</button></figure>
        </div>
        <label v-if="canManage" class="secondary-button action-upload">{{ t('packing.addOrChangePhoto') }}<input type="file" accept="image/jpeg,image/png,image/webp" multiple @change="addPhotos($event, { bagId: gallery.bagId, itemId: gallery.itemId })" /></label>
      </div>
    </AppDialog>

    <AppDialog v-if="editor" :label="t(`packing.editor.${editor.kind}`)" @close="editor = null">
      <form class="stack-form" @submit.prevent="saveEditor"><h2>{{ t(`packing.editor.${editor.kind}`) }}</h2><label><span>{{ t('packing.name') }}</span><input v-model="editor.name" maxlength="200" autofocus required /></label><div class="dialog-actions"><button class="secondary-button" type="button" @click="editor = null">{{ t('common.cancel') }}</button><button class="primary-button" type="submit">{{ t('common.save') }}</button></div></form>
    </AppDialog>

    <AppDialog v-if="confirming" :label="confirming.type === 'reset' ? t('packing.resetTitle') : confirming.type === 'conflict' ? t('packing.discardTitle') : t('packing.deleteTitle')" @close="confirming = null">
      <form class="stack-form" @submit.prevent="confirmAction"><h2>{{ confirming.type === 'reset' ? t('packing.resetTitle') : confirming.type === 'conflict' ? t('packing.discardTitle') : t('packing.deleteTitle') }}</h2><p>{{ confirming.type === 'reset' ? t('packing.resetConfirm') : confirming.type === 'conflict' ? t('packing.discardConfirm') : t('packing.deleteConfirm', { name: confirming.name }) }}</p><div class="dialog-actions"><button class="secondary-button" type="button" @click="confirming = null">{{ t('common.cancel') }}</button><button class="danger-button" type="submit">{{ confirming.type === 'reset' ? t('packing.reset') : confirming.type === 'conflict' ? t('packing.discard') : t('common.delete') }}</button></div></form>
    </AppDialog>
  </main>
</template>

<style scoped>
.packing-page { display: grid; gap: 1rem; max-width: 980px; }
.packing-heading h1 { margin: .15rem 0; }
.packing-heading p { margin-bottom: 0; }
.packing-overview { display: grid; grid-template-columns: minmax(9rem, auto) minmax(12rem, 1fr) auto; align-items: center; gap: 12px; padding: 14px; border: 1px solid var(--border-default); border-radius: var(--radius-panel); background: var(--surface-panel); }
.overview-copy { display: flex; flex-wrap: wrap; align-items: baseline; gap: 5px 10px; }
.overview-copy strong { width: 100%; }
.overview-copy span, .overview-copy small { color: var(--text-secondary); }
.progress-track { height: 22px; overflow: hidden; border: 1px solid var(--danger-border); border-radius: var(--radius-control); background: var(--danger-soft); }
.progress-track span { display: block; height: 100%; background: var(--success-text); transition: width .2s ease; }
.packing-message { display: flex; justify-content: space-between; gap: 1rem; padding: .8rem 1rem; border: 1px solid var(--warning-border); border-radius: var(--radius-panel); color: var(--warning-text); background: var(--warning-soft); }
.packing-message button { border: 0; background: transparent; text-decoration: underline; }
.packing-conflicts { display: grid; gap: .6rem; padding: 1rem; border: 1px solid var(--warning-border); border-radius: var(--radius-panel); background: var(--surface-panel); }
.packing-conflicts h2, .packing-conflicts p { margin: 0; }
.packing-conflicts article { display: grid; grid-template-columns: minmax(8rem, 1fr) minmax(10rem, 2fr) auto; align-items: center; gap: .75rem; }
.packing-conflicts small { color: var(--text-secondary); }
.row-actions, .dialog-actions, .action-move-row { display: flex; flex-wrap: wrap; align-items: center; gap: .5rem; }
.bag-list { display: grid; gap: 10px; }
.packing-bag { overflow: hidden; border: 1px solid var(--border-default); border-radius: var(--radius-panel); background: var(--surface-panel); }
.packing-bag.excluded { border-style: dashed; }
.bag-row { display: grid; grid-template-columns: minmax(0, 1fr) 48px 44px 48px; align-items: stretch; min-height: 58px; }
.bag-progress-button { position: relative; isolation: isolate; display: grid; align-content: center; gap: 2px; min-width: 0; padding: 9px 14px; overflow: hidden; border: 0; color: var(--text-primary); background: var(--danger-soft); text-align: left; }
.bag-progress-button::before { position: absolute; inset: 0 auto 0 0; z-index: -1; width: var(--bag-progress); background: var(--success-soft); content: ''; transition: width .2s ease; }
.bag-progress-button.excluded { background: var(--surface-inset); }
.bag-progress-button.excluded::before { display: none; }
.bag-progress-button:hover:not(:disabled) { box-shadow: inset 0 0 0 1px var(--accent); }
.bag-name { overflow: hidden; font-size: 1.05rem; font-weight: 760; text-overflow: ellipsis; white-space: nowrap; }
.bag-progress-button small { color: var(--text-secondary); }
.collapse-button, .more-button, .photo-trigger { display: grid; min-width: 44px; min-height: 44px; place-items: center; padding: 4px; border: 0; border-left: 1px solid var(--border-subtle); color: var(--text-primary); background: var(--surface-subtle); }
.collapse-button:hover, .more-button:hover, .photo-trigger:hover { background: var(--surface-hover); }
.collapse-button svg { width: 24px; fill: none; stroke: currentColor; stroke-linecap: round; stroke-linejoin: round; stroke-width: 2; transition: transform .15s ease; }
.collapse-button svg.collapsed { transform: rotate(-90deg); }
.more-button { font-size: 1rem; font-weight: 800; letter-spacing: -2px; }
.photo-trigger { position: relative; cursor: pointer; }
.photo-trigger svg { width: 23px; fill: currentColor; }
.photo-trigger input, .action-upload input { position: absolute; width: 1px; height: 1px; opacity: 0; }
.photo-trigger:focus-within, .action-upload:focus-within { outline: 3px solid var(--focus-ring); outline-offset: 2px; }
.photo-trigger.has-photo { overflow: hidden; padding: 3px; }
.photo-trigger img { width: 100%; height: 100%; min-height: 40px; object-fit: cover; border-radius: var(--radius-small); }
.photo-trigger > span { position: absolute; right: 2px; bottom: 2px; padding: 1px 4px; border-radius: var(--radius-small); color: var(--on-accent); background: var(--accent); font-size: .68rem; font-weight: 800; }
.photo-trigger.disabled { opacity: .35; cursor: default; }
.bag-body { display: grid; gap: 8px; padding: 8px 10px 10px 28px; border-top: 1px solid var(--border-subtle); }
.item-list { display: grid; gap: 6px; }
.item-list.muted { opacity: .64; }
.item-row { display: grid; grid-template-columns: minmax(0, 1fr) 48px 44px; min-height: 52px; overflow: hidden; border: 1px solid var(--border-subtle); border-radius: var(--radius-control); background: var(--danger-soft); }
.item-row.state-packed { background: var(--success-soft); }
.item-row.state-stays_here { background: var(--surface-inset); border-style: dashed; }
.item-toggle { display: grid; grid-template-columns: 24px minmax(0, 1fr) auto; align-items: center; gap: 8px; min-width: 0; padding: 8px 12px; border: 0; color: var(--text-primary); background: transparent; text-align: left; }
.item-toggle:hover:not(:disabled) { background: color-mix(in srgb, var(--surface-hover) 55%, transparent); }
.item-toggle > span:nth-child(2) { overflow: hidden; font-weight: 700; text-overflow: ellipsis; white-space: nowrap; }
.item-toggle small { color: var(--text-secondary); }
.state-icon { font-size: 1.1rem; font-weight: 900; text-align: center; }
.add-row-button { display: flex; width: fit-content; min-height: 44px; align-items: center; gap: 8px; padding: 4px 10px; border: 0; border-radius: var(--radius-control); color: var(--text-secondary); background: transparent; }
.add-row-button:hover { color: var(--text-primary); background: var(--surface-hover); }
.add-row-button span:first-child { font-size: 1.8rem; line-height: 1; }
.add-bag-button { justify-self: center; }
.packing-action-menu { min-width: min(22rem, 80vw); }
.packing-action-menu > button, .packing-action-menu > label { width: 100%; justify-content: center; }
.action-upload { position: relative; display: inline-flex; align-items: center; cursor: pointer; }
.action-move-row > button { flex: 1; }
.photo-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(9rem, 1fr)); gap: 10px; }
.photo-grid figure { position: relative; margin: 0; min-width: 0; }
.photo-grid img { width: 100%; aspect-ratio: 4 / 3; object-fit: cover; border-radius: var(--radius-control); background: var(--surface-inset); }
.photo-grid figcaption { overflow: hidden; margin-top: 3px; color: var(--text-secondary); font-size: .75rem; text-overflow: ellipsis; white-space: nowrap; }
.photo-grid figure > button { position: absolute; top: 5px; right: 5px; width: 32px; height: 32px; border: 1px solid var(--danger-border); border-radius: 50%; color: var(--danger-text); background: var(--danger-soft); }
@media (max-width: 680px) {
  .packing-overview { grid-template-columns: 1fr auto; }
  .progress-track { grid-column: 1 / -1; grid-row: 2; }
  .bag-row { grid-template-columns: minmax(0, 1fr) 46px 44px 46px; }
  .bag-body { padding-left: 12px; }
  .item-toggle { grid-template-columns: 22px minmax(0, 1fr); }
  .item-toggle small { grid-column: 2; }
  .packing-conflicts article { grid-template-columns: 1fr; }
}
</style>
