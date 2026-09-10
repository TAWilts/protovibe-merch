import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { watch } from 'vue'

import App from './App.vue'
import router from './router'
import { i18n } from './i18n'
import '@fontsource-variable/geist/wght.css'
import './assets/base.css'
// After base.css on purpose: the touch sizes have to win the cascade over the
// desktop defaults they replace, and a media query adds no specificity.
import './assets/tablet.css'

import { useOfflineStore } from './stores/offline'
import { usePackingStore } from './stores/packing'
import { useSessionStore } from './stores/session'
import { setUnauthorizedHandler } from './api/client'

const app = createApp(App)
const pinia = createPinia()
app.use(pinia).use(router).use(i18n)

const packingStore = usePackingStore(pinia)
const sessionStore = useSessionStore(pinia)
watch(() => sessionStore.identity, (identity) => {
  if (identity?.band) {
    void packingStore.prepare(identity)
  } else {
    packingStore.resetSession()
  }
}, { immediate: true })

setUnauthorizedHandler(() => {
  if (!sessionStore.isAuthenticated) return
  const previousPath = router.currentRoute.value.fullPath
  sessionStore.clear()
  if (router.currentRoute.value.name !== 'login') {
    void router.replace({ name: 'login', query: { next: previousPath } })
  }
})

app.mount('#app')

// Wired after mounting so the connection listeners and the first queue flush
// do not delay the first paint.
useOfflineStore(pinia).start()
packingStore.start()

/**
 * Installs the service worker that carries the app shell.
 *
 * Without this the worker is built and shipped but never activated, which
 * leaves the offline queue able to hold a sale yet the app unable to start at
 * a gig with no signal — the one situation the PWA exists for. Registration is
 * deliberate rather than injected, so it stays visible here.
 */
if ('serviceWorker' in navigator && import.meta.env.PROD) {
  window.addEventListener('load', () => {
    void navigator.serviceWorker.register('/service-worker.js').catch((error) => {
      // A refused registration (private window, unsupported browser) must
      // never take the app down with it; online use is unaffected.
      console.warn('service worker registration failed', error)
    })
  })
}
