import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'

import { useSessionStore } from '@/stores/session'
import type { FeatureFlags } from '@/api/types'
import { setLocale } from '@/i18n'

/**
 * The public landing page and two authenticated shells share one router: the
 * band app at `/app` and the platform admin center at `/admin`.
 *
 * The guards below decide what to render; the server enforces every permission
 * again independently, so a tampered client gains nothing by editing this table.
 */
const routes: RouteRecordRaw[] = [
  {
    path: '/',
    name: 'landing',
    component: () => import('@/views/LandingView.vue'),
    meta: { public: true, immediate: true },
  },
  {
    path: '/login',
    name: 'login',
    component: () => import('@/views/auth/LoginView.vue'),
    meta: { public: true },
  },
  {
    path: '/app',
    component: () => import('@/components/layout/AppShell.vue'),
    children: [
      { path: '', redirect: '/sales' },
      { path: '/sales', name: 'sales', component: () => import('@/views/band/SalesView.vue') },
      // The same terminal in order mode: a sale that is not handed over now.
      { path: '/orders', name: 'orders', component: () => import('@/views/band/SalesView.vue') },
      { path: '/history', name: 'history', component: () => import('@/views/band/HistoryView.vue') },
      { path: '/operations', name: 'operations', component: () => import('@/views/band/OperationsView.vue') },
      { path: '/slideshow', name: 'slideshow', component: () => import('@/views/band/SlideshowView.vue'), meta: { feature: 'slideshow' } },
      { path: '/articles', name: 'articles', component: () => import('@/views/band/ArticlesView.vue') },
      { path: '/purchases', name: 'purchases', component: () => import('@/views/band/PurchasesView.vue') },
      { path: '/band-finances', name: 'band-finances', component: () => import('@/views/band/BandFinancesView.vue'), meta: { feature: 'band_finances' } },
      { path: '/balances', name: 'balances', component: () => import('@/views/band/BalancesView.vue') },
      { path: '/administration', name: 'administration', component: () => import('@/views/band/AdministrationView.vue') },
      { path: '/profile', name: 'profile', component: () => import('@/views/band/ProfileView.vue') },
    ],
  },
  {
    // The admin center is its own shell: a platform account has no band data
    // at all unless a support grant is live, so the band navigation would only
    // offer locked doors.
    path: '/admin',
    component: () => import('@/components/layout/PlatformShell.vue'),
    children: [
      { path: '', redirect: { name: 'platform-bands' } },
      { path: 'bands', name: 'platform-bands', component: () => import('@/views/platform/BandsView.vue') },
      { path: 'registrations', name: 'platform-registrations', component: () => import('@/views/platform/RegistrationsView.vue'), meta: { systemAdmin: true } },
      { path: 'users', name: 'platform-users', component: () => import('@/views/platform/UsersView.vue'), meta: { systemAdmin: true } },
      { path: 'support-access', name: 'platform-support', component: () => import('@/views/platform/SupportAccessView.vue') },
      { path: 'messages', name: 'platform-messages', component: () => import('@/views/platform/MessagesView.vue') },
      { path: 'audit', name: 'platform-audit', component: () => import('@/views/platform/AuditView.vue') },
      { path: 'backups', name: 'platform-backups', component: () => import('@/views/platform/BackupsView.vue') },
      { path: 'settings', name: 'platform-settings', component: () => import('@/views/platform/SettingsView.vue') },
    ],
  },
  {
    path: '/:pathMatch(.*)*',
    name: 'not-found',
    component: () => import('@/views/NotFoundView.vue'),
    meta: { public: true },
  },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
  scrollBehavior: (to) => {
    if (/^#(top|features|workflow|register)$/.test(to.hash)) {
      return { el: to.hash, behavior: 'smooth' }
    }
    return { top: 0 }
  },
})

router.beforeEach(async (to) => {
  const session = useSessionStore()

  if (to.meta.immediate) {
    if (!session.ready && !session.loading) void session.restore(false).catch(() => undefined)
    return true
  }

  // The session is restored once per page load, before the first guarded
  // navigation, so a reload does not bounce the user through the login page.
  if (!session.ready) {
    await session.restore()
  }

  if (to.meta.public) {
    if (to.name === 'login' && session.isAuthenticated) {
      // Send them to the shell they can actually use. A platform account has
      // no band data, so bouncing it through the sales page only to redirect
      // again made switching accounts look broken.
      const caps = session.capabilities
      return caps?.is_platform_staff ? { name: 'platform-bands' } : { name: 'sales' }
    }
    return true
  }

  if (!session.isAuthenticated) {
    return { name: 'login', query: { next: to.fullPath } }
  }

  const caps = session.capabilities
  const isPlatformRoute = String(to.name ?? '').startsWith('platform-')

  // A platform account has no band data at all unless a support grant is live,
  // so it lands in the admin center rather than on an empty sales page. With a
  // grant it may go where the grant reaches — the server decides either way,
  // this only avoids offering doors that are locked.
  const hasBandAccess = caps?.can_access_band_workflows || session.supportGrant !== null
  if (caps && !hasBandAccess && !isPlatformRoute && to.name !== 'profile') {
    return { name: 'platform-bands' }
  }
  // The reverse: a band account has nothing to do in the control plane.
  if (caps && isPlatformRoute && !caps.can_access_system_administration) {
    return { name: 'sales' }
  }

  // A public marketing-language choice must not leak into the authenticated
  // application. Account preferences remain authoritative there.
  setLocale('de')
  if (to.meta.systemAdmin && !caps?.is_system_admin) {
    return { name: 'platform-bands' }
  }

  const requiredFeature = to.meta.feature as keyof FeatureFlags | undefined
  if (requiredFeature && session.featureFlags?.[requiredFeature] === false) {
    return { name: 'sales' }
  }

  return true
})

export default router
