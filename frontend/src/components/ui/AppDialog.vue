<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, useId } from 'vue'

const props = withDefaults(defineProps<{
  label: string
  dismissible?: boolean
}>(), {
  dismissible: true,
})

const emit = defineEmits<{
  close: []
}>()

const dialog = ref<HTMLDialogElement | null>(null)
const titleId = useId()
let returnFocus: HTMLElement | null = null

function focusDialog() {
  const element = dialog.value
  if (!element || element.contains(document.activeElement)) return
  const target = element.querySelector<HTMLElement>(
    '[autofocus], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), button:not([disabled]), [href], [tabindex]:not([tabindex="-1"])',
  )
  ;(target ?? element).focus()
}

function requestClose(event?: Event) {
  event?.preventDefault()
  if (props.dismissible) emit('close')
}

function onBackdropClick(event: MouseEvent) {
  if (event.target === dialog.value) requestClose(event)
}

onMounted(async () => {
  returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
  const element = dialog.value
  if (!element) return
  if (typeof element.showModal === 'function') element.showModal()
  else element.setAttribute('open', '')
  await nextTick()
  focusDialog()
})

onBeforeUnmount(() => {
  const target = returnFocus
  queueMicrotask(() => {
    // A successful action may replace one modal with another (for example a
    // setup form with its one-time code). In that case the new top-layer
    // dialog owns focus and must not lose it to the original page trigger.
    if (!document.querySelector('dialog[open]') && target?.isConnected) target.focus()
  })
})
</script>

<template>
  <dialog
    ref="dialog"
    class="confirmation-dialog"
    :aria-labelledby="titleId"
    tabindex="-1"
    @cancel="requestClose"
    @close="requestClose"
    @click="onBackdropClick"
  >
    <span :id="titleId" class="visually-hidden">{{ label }}</span>
    <slot />
  </dialog>
</template>
