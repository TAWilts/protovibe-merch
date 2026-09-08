<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'

import type { PackingBag, PackingItem, PackingStatus } from '@/api/types'
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
const draggedBag = ref('')
const draggedItem = ref<{ bagId: string; itemId: string } | null>(null)

const collapseKey = computed(() => `protovibe.packing.collapsed.v1:${session.band?.id ?? 0}`)
const totals = computed(() => {
  let packed = 0
  let stays = 0
  let open = 0
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
      if (item.status === 'packed') packed++
      else if (item.status === 'stays_here') stays++
      else open++
    }
  }
  return { packed, stays, open, all: packed + stays + open }
})

onMounted(async () => {
  loadCollapsed()
  if (session.identity) await packing.activate(session.identity)
})
onUnmounted(() => packing.deactivatePage())

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
  if (bag.status === 'stays_here') return { packed: 0, stays: Math.max(1, bag.items.length), open: 0 }
  if (!bag.items.length) return {
    packed: bag.status === 'packed' ? 1 : 0,
    stays: 0,
    open: bag.status === 'open' ? 1 : 0,
  }
  return bag.items.reduce((result, item) => {
    result[item.status === 'packed' ? 'packed' : item.status === 'stays_here' ? 'stays' : 'open']++
    return result
  }, { packed: 0, stays: 0, open: 0 })
}

function bagPartial(bag: PackingBag) {
  const counts = bagCounts(bag)
  return bag.status === 'open' && counts.open > 0 && (counts.packed + counts.stays) > 0
}

async function mutate(type: Parameters<typeof packing.mutate>[0], payload = {}) {
  try {
    await packing.mutate(type, payload)
  } catch {
    flash.error(t('packing.localError'))
  }
}

function toggleBag(bag: PackingBag) {
  const status: PackingStatus = bag.status === 'packed' ? 'open' : 'packed'
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
  if (entry.kind === 'create_bag') {
    await mutate('create_bag', { bag_id: crypto.randomUUID(), name })
  } else if (entry.kind === 'rename_bag') {
    await mutate('rename_bag', { bag_id: entry.bagId, name })
  } else if (entry.kind === 'create_item') {
    await mutate('create_item', { bag_id: entry.bagId, item_id: crypto.randomUUID(), name })
  } else {
    await mutate('rename_item', { item_id: entry.itemId, name })
  }
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
  const sourceId = draggedBag.value
  draggedBag.value = ''
  if (!sourceId || sourceId === targetId) return
  const order = packing.snapshot.bags.map((bag) => bag.id).filter((id) => id !== sourceId)
  order.splice(order.indexOf(targetId), 0, sourceId)
  void mutate('reorder_bags', { order_ids: order })
}

function dropItem(bag: PackingBag, targetId: string) {
  const source = draggedItem.value
  draggedItem.value = null
  if (!source || source.bagId !== bag.id || source.itemId === targetId) return
  const order = bag.items.map((item) => item.id).filter((id) => id !== source.itemId)
  order.splice(order.indexOf(targetId), 0, source.itemId)
  void mutate('reorder_items', { bag_id: bag.id, order_ids: order })
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
      <div class="packing-toolbar">
        <span class="sync-state" :class="{ offline: !packing.online }">
          {{ packing.online ? (packing.syncing ? t('sync.syncing') : t('sync.online')) : t('sync.offline') }}
          <template v-if="packing.queued"> · {{ t('packing.pending', { count: packing.queued }) }}</template>
        </span>
        <button class="secondary-button" type="button" :disabled="packing.syncing || !packing.online" @click="packing.sync()">
          {{ t('packing.syncNow') }}
        </button>
        <button v-if="canManage" class="secondary-button" type="button" @click="confirming = { type: 'reset' }">
          {{ t('packing.reset') }}
        </button>
        <button v-if="canManage" class="primary-button" type="button" @click="openEditor('create_bag')">
          {{ t('packing.addBag') }}
        </button>
      </div>
    </header>

    <section class="packing-progress" :aria-label="t('packing.progress')">
      <strong>{{ totals.packed }} {{ t('packing.packed') }}</strong>
      <span>{{ totals.stays }} {{ t('packing.staysHere') }}</span>
      <span>{{ totals.open }} {{ t('packing.open') }}</span>
      <progress :value="totals.packed + totals.stays" :max="Math.max(1, totals.all)"></progress>
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
          <button type="button" :disabled="!packing.online" @click="confirming = { type: 'conflict', id: conflict.key, name: conflict.operation?.name || conflict.photo?.filename }">{{ t('packing.discard') }}</button>
        </div>
      </article>
    </section>

    <p v-if="packing.loading && !packing.snapshot.bags.length" class="empty-state">{{ t('common.loading') }}</p>
    <p v-else-if="!packing.snapshot.bags.length" class="empty-state">{{ t('packing.empty') }}</p>

    <section class="bag-list">
      <article
        v-for="(bag, bagIndex) in packing.snapshot.bags"
        :key="bag.id"
        class="packing-bag"
        :class="{ excluded: bag.status === 'stays_here' }"
        :draggable="canManage"
        @dragstart="draggedBag = bag.id"
        @dragover.prevent
        @drop.prevent="dropBag(bag.id)"
      >
        <header class="bag-header">
          <button class="collapse-button" type="button" :aria-expanded="!collapsed.has(bag.id)" @click="toggleCollapsed(bag.id)">
            <span aria-hidden="true">{{ collapsed.has(bag.id) ? '›' : '⌄' }}</span>
            <span class="bag-name">{{ bag.name }}</span>
          </button>
          <span class="bag-counts">
            {{ bagCounts(bag).packed }}/{{ bagCounts(bag).packed + bagCounts(bag).stays + bagCounts(bag).open }}
            <span v-if="bagCounts(bag).stays"> · {{ bagCounts(bag).stays }} {{ t('packing.staysShort') }}</span>
          </span>
          <label class="status-check">
            <input
              type="checkbox"
              :checked="bag.status === 'packed'"
              :indeterminate.prop="bagPartial(bag)"
              :disabled="bag.status === 'stays_here'"
              @change="toggleBag(bag)"
            />
            <span>{{ t('packing.bagPacked') }}</span>
          </label>
          <button class="stay-button" :class="{ active: bag.status === 'stays_here' }" type="button" @click="toggleBagStays(bag)">
            {{ t('packing.staysHere') }}
          </button>
          <div v-if="canManage" class="row-actions">
            <button type="button" :aria-label="t('packing.moveUp')" :disabled="bagIndex === 0" @click="moveBag(bag.id, -1)">↑</button>
            <button type="button" :aria-label="t('packing.moveDown')" :disabled="bagIndex === packing.snapshot.bags.length - 1" @click="moveBag(bag.id, 1)">↓</button>
            <button type="button" @click="openEditor('rename_bag', bag)">{{ t('packing.rename') }}</button>
            <button type="button" @click="confirming = { type: 'bag', id: bag.id, name: bag.name }">{{ t('common.delete') }}</button>
          </div>
        </header>

        <div v-if="!collapsed.has(bag.id)" class="bag-body">
          <div v-if="bag.photos.length" class="photo-grid">
            <figure v-for="photo in bag.photos" :key="photo.id">
              <img v-if="packing.photoURL(photo.id)" :src="packing.photoURL(photo.id)" :alt="photo.original_filename" />
              <figcaption>{{ photo.original_filename }}</figcaption>
              <button v-if="canManage" type="button" @click="confirming = { type: 'photo', id: photo.id, name: photo.original_filename }">×</button>
            </figure>
          </div>
          <label v-if="canManage" class="photo-upload secondary-button">
            {{ t('packing.addBagPhotos') }}
            <input type="file" accept="image/jpeg,image/png,image/webp" multiple @change="addPhotos($event, { bagId: bag.id })" />
          </label>

          <div class="item-list" :class="{ muted: bag.status === 'stays_here' }">
            <article
              v-for="(item, itemIndex) in bag.items"
              :key="item.id"
              class="packing-item"
              :draggable="canManage"
              @dragstart.stop="draggedItem = { bagId: bag.id, itemId: item.id }"
              @dragover.prevent
              @drop.prevent.stop="dropItem(bag, item.id)"
            >
              <div class="item-line">
                <label class="status-check item-check">
                  <input type="checkbox" :checked="item.status === 'packed'" :disabled="bag.status === 'stays_here' || item.status === 'stays_here'" @change="toggleItem(item)" />
                  <span>{{ item.name }}</span>
                </label>
                <button class="stay-button" :class="{ active: item.status === 'stays_here' }" type="button" :disabled="bag.status === 'stays_here'" @click="toggleItemStays(item)">
                  {{ t('packing.staysHere') }}
                </button>
                <div v-if="canManage" class="row-actions">
                  <button type="button" :aria-label="t('packing.moveUp')" :disabled="itemIndex === 0" @click="moveItem(bag, item.id, -1)">↑</button>
                  <button type="button" :aria-label="t('packing.moveDown')" :disabled="itemIndex === bag.items.length - 1" @click="moveItem(bag, item.id, 1)">↓</button>
                  <button type="button" @click="openEditor('rename_item', bag, item)">{{ t('packing.rename') }}</button>
                  <button type="button" @click="confirming = { type: 'item', id: item.id, name: item.name }">{{ t('common.delete') }}</button>
                </div>
              </div>
              <div v-if="item.photos.length" class="photo-grid item-photos">
                <figure v-for="photo in item.photos" :key="photo.id">
                  <img v-if="packing.photoURL(photo.id)" :src="packing.photoURL(photo.id)" :alt="photo.original_filename" />
                  <figcaption>{{ photo.original_filename }}</figcaption>
                  <button v-if="canManage" type="button" @click="confirming = { type: 'photo', id: photo.id, name: photo.original_filename }">×</button>
                </figure>
              </div>
              <label v-if="canManage" class="photo-upload subtle-upload">
                {{ t('packing.addItemPhotos') }}
                <input type="file" accept="image/jpeg,image/png,image/webp" multiple @change="addPhotos($event, { itemId: item.id })" />
              </label>
            </article>
          </div>

          <button v-if="canManage" class="secondary-button add-item" type="button" @click="openEditor('create_item', bag)">
            {{ t('packing.addItem') }}
          </button>
        </div>
      </article>
    </section>

    <dialog v-if="editor" class="confirmation-dialog" open>
      <form class="stack-form" @submit.prevent="saveEditor">
        <h2>{{ t(`packing.editor.${editor.kind}`) }}</h2>
        <label>
          <span>{{ t('packing.name') }}</span>
          <input v-model="editor.name" maxlength="200" autofocus required />
        </label>
        <div class="dialog-actions">
          <button class="secondary-button" type="button" @click="editor = null">{{ t('common.cancel') }}</button>
          <button class="primary-button" type="submit">{{ t('common.save') }}</button>
        </div>
      </form>
    </dialog>

    <dialog v-if="confirming" class="confirmation-dialog" open>
      <form class="stack-form" @submit.prevent="confirmAction">
        <h2>{{ confirming.type === 'reset' ? t('packing.resetTitle') : confirming.type === 'conflict' ? t('packing.discardTitle') : t('packing.deleteTitle') }}</h2>
        <p>{{ confirming.type === 'reset' ? t('packing.resetConfirm') : confirming.type === 'conflict' ? t('packing.discardConfirm') : t('packing.deleteConfirm', { name: confirming.name }) }}</p>
        <div class="dialog-actions">
          <button class="secondary-button" type="button" @click="confirming = null">{{ t('common.cancel') }}</button>
          <button class="danger-button" type="submit">{{ confirming.type === 'reset' ? t('packing.reset') : confirming.type === 'conflict' ? t('packing.discard') : t('common.delete') }}</button>
        </div>
      </form>
    </dialog>
  </main>
</template>

<style scoped>
.packing-page { display: grid; gap: 1rem; }
.packing-heading { display: flex; justify-content: space-between; align-items: flex-end; gap: 1rem; }
.packing-heading h1 { margin: .15rem 0; }
.packing-heading p { margin-bottom: 0; }
.packing-toolbar, .row-actions, .dialog-actions { display: flex; flex-wrap: wrap; align-items: center; gap: .5rem; }
.sync-state { font-size: .86rem; color: var(--muted); }
.sync-state.offline { color: var(--warning); }
.packing-progress { display: grid; grid-template-columns: repeat(3, auto) 1fr; align-items: center; gap: .75rem 1rem; padding: 1rem; border: 1px solid var(--border-subtle); border-radius: var(--radius-panel); background: var(--surface-panel); }
.packing-progress progress { width: 100%; min-width: 8rem; accent-color: var(--accent); }
.packing-message { display: flex; justify-content: space-between; gap: 1rem; padding: .8rem 1rem; border: 1px solid var(--warning-border); border-radius: var(--radius-panel); color: var(--warning-text); background: var(--warning-soft); }
.packing-message button { border: 0; background: transparent; text-decoration: underline; }
.packing-conflicts { display: grid; gap: .6rem; padding: 1rem; border: 1px solid var(--warning-border); border-radius: var(--radius-panel); background: var(--surface-panel); }
.packing-conflicts h2, .packing-conflicts p { margin: 0; }
.packing-conflicts article { display: grid; grid-template-columns: minmax(8rem, 1fr) minmax(10rem, 2fr) auto; align-items: center; gap: .75rem; }
.packing-conflicts small { color: var(--muted); }
.bag-list { display: grid; gap: .85rem; }
.packing-bag { overflow: hidden; border: 1px solid var(--border-default); border-radius: var(--radius-panel); background: var(--surface-panel); }
.packing-bag.excluded { opacity: .72; }
.bag-header { display: flex; flex-wrap: wrap; align-items: center; gap: .65rem; padding: .85rem 1rem; }
.collapse-button { display: flex; align-items: center; gap: .55rem; flex: 1 1 12rem; padding: .3rem 0; border: 0; background: transparent; text-align: left; font: inherit; }
.collapse-button > span:first-child { font-size: 1.5rem; width: 1rem; }
.bag-name { font-size: 1.08rem; font-weight: 750; }
.bag-counts { color: var(--muted); font-size: .84rem; }
.status-check { display: inline-flex; align-items: center; gap: .45rem; font-weight: 650; }
.status-check input { width: 1.2rem; height: 1.2rem; accent-color: var(--accent); }
.stay-button, .row-actions button { min-height: 2.25rem; padding: .35rem .65rem; border: 1px solid var(--border-default); border-radius: var(--radius-control); color: var(--text-primary); background: var(--surface-subtle); transition: border-color .15s, background .15s, transform .12s; }
.stay-button:hover:not(:disabled), .row-actions button:hover:not(:disabled) { border-color: var(--accent); background: var(--surface-hover); }
.stay-button:active:not(:disabled), .row-actions button:active:not(:disabled) { background: var(--surface-pressed); transform: translateY(1px); }
.stay-button:disabled, .row-actions button:disabled { opacity: .48; cursor: not-allowed; }
.stay-button.active { color: var(--on-accent); background: var(--accent); border-color: var(--accent-hover); }
.bag-body { display: grid; gap: .8rem; padding: 0 1rem 1rem 2.6rem; }
.item-list { display: grid; gap: .55rem; }
.item-list.muted { opacity: .65; }
.packing-item { padding: .7rem .75rem; border: 1px solid var(--border-subtle); border-radius: var(--radius-panel); background: var(--surface-subtle); }
.item-line { display: flex; align-items: center; gap: .6rem; }
.item-check { flex: 1 1 12rem; }
.photo-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(8rem, 1fr)); gap: .65rem; }
.photo-grid figure { position: relative; margin: 0; min-width: 0; }
.photo-grid img { width: 100%; aspect-ratio: 4 / 3; object-fit: cover; border-radius: var(--radius-control); background: var(--surface-inset); }
.photo-grid figcaption { overflow: hidden; margin-top: .2rem; color: var(--muted); font-size: .75rem; text-overflow: ellipsis; white-space: nowrap; }
.photo-grid figure > button { position: absolute; top: .3rem; right: .3rem; width: 1.8rem; height: 1.8rem; border: 0; border-radius: 50%; color: white; background: rgba(20, 25, 23, .8); }
.item-photos { margin-top: .65rem; grid-template-columns: repeat(auto-fill, minmax(6rem, 9rem)); }
.photo-upload { width: fit-content; cursor: pointer; }
.photo-upload input { position: absolute; width: 1px; height: 1px; opacity: 0; }
.subtle-upload { display: inline-block; margin-top: .55rem; color: var(--muted); font-size: .8rem; text-decoration: underline; }
.add-item { justify-self: start; }
.empty-state { padding: 2rem; text-align: center; color: var(--muted); }
.danger-button { padding: .7rem 1rem; border: 1px solid var(--danger-border); border-radius: var(--radius-control); color: var(--danger-text); background: var(--danger-soft); }
@media (max-width: 760px) {
  .packing-heading { align-items: stretch; flex-direction: column; }
  .packing-progress { grid-template-columns: 1fr 1fr 1fr; }
  .packing-progress progress { grid-column: 1 / -1; }
  .bag-body { padding-left: 1rem; }
  .item-line { align-items: flex-start; flex-wrap: wrap; }
  .row-actions { width: 100%; }
  .packing-conflicts article { grid-template-columns: 1fr; }
}
</style>
