import { defineStore } from 'pinia'
import { ref } from 'vue'

export type FlashKind = 'success' | 'error'

export interface FlashMessage {
  id: number
  kind: FlashKind
  text: string
}

let nextId = 1

/**
 * Transient notices, the direct equivalent of the original's flash messages.
 * They auto-dismiss so a seller at a stand never has to clear them by hand.
 */
export const useFlashStore = defineStore('flash', () => {
  const messages = ref<FlashMessage[]>([])

  function push(kind: FlashKind, text: string, timeoutMs = 6000) {
    const id = nextId++
    messages.value.push({ id, kind, text })
    if (timeoutMs > 0) {
      window.setTimeout(() => dismiss(id), timeoutMs)
    }
  }

  function dismiss(id: number) {
    messages.value = messages.value.filter((message) => message.id !== id)
  }

  return {
    messages,
    success: (text: string) => push('success', text),
    error: (text: string) => push('error', text, 10000),
    dismiss,
  }
})
