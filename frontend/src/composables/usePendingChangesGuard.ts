import { onBeforeUnmount, onMounted, toValue, type MaybeRefOrGetter } from 'vue'
import { onBeforeRouteLeave } from 'vue-router'

/**
 * Protects short-lived view state such as a till basket or an unsaved article
 * configuration. In-app navigation can show the translated explanation;
 * browsers use their own standard message for reloads and closing the tab.
 */
export function usePendingChangesGuard(
  pending: MaybeRefOrGetter<boolean>,
  message: MaybeRefOrGetter<string>,
) {
  const confirmLeave = () => !toValue(pending) || window.confirm(toValue(message))

  onBeforeRouteLeave(confirmLeave)

  const beforeUnload = (event: BeforeUnloadEvent) => {
    if (!toValue(pending)) return
    event.preventDefault()
    event.returnValue = ''
  }

  onMounted(() => window.addEventListener('beforeunload', beforeUnload))
  onBeforeUnmount(() => window.removeEventListener('beforeunload', beforeUnload))

  return { confirmLeave }
}
