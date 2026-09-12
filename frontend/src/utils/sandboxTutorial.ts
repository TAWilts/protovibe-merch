import type { SandboxIdentity } from '@/api/types'

export const sandboxTutorialSteps = [
  { key: 'catalogue', route: 'sandbox-articles', nav: 'articles' },
  { key: 'purchase', route: 'sandbox-purchases', nav: 'purchases' },
  { key: 'sale', route: 'sandbox-sales', nav: 'sales' },
  { key: 'balance', route: 'sandbox-balances', nav: 'balances' },
] as const

export const sandboxExplorationSteps = [
  { key: 'shipping', route: 'sandbox-sales' },
  { key: 'cancellation', route: 'sandbox-history' },
  { key: 'slideshow', route: 'sandbox-slideshow' },
  { key: 'packing', route: 'sandbox-packing-list' },
  { key: 'roles', route: null },
] as const

export type SandboxTutorialStep = (typeof sandboxTutorialSteps)[number]
export type SandboxExplorationStep = (typeof sandboxExplorationSteps)[number]

export function sandboxTutorialComplete(
  sandbox: SandboxIdentity | null | undefined,
): boolean {
  return sandboxTutorialSteps.every((step) => sandbox?.tutorial_state[step.key] === true)
}

/**
 * Returns the one route currently available in the guided sandbox. Hiding the
 * tutorial or completing every step removes the restriction entirely.
 */
export function nextSandboxTutorialStep(
  sandbox: SandboxIdentity | null | undefined,
): SandboxTutorialStep | null {
  if (!sandbox?.tutorial_visible) return null
  return sandboxTutorialSteps.find((step) => sandbox.tutorial_state[step.key] !== true) ?? null
}
