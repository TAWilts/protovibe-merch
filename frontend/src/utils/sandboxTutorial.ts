import type { SandboxIdentity } from '@/api/types'

export const sandboxTutorialSteps = [
  { key: 'catalogue', route: 'sandbox-articles', nav: 'articles' },
  { key: 'purchase', route: 'sandbox-purchases', nav: 'purchases' },
  { key: 'sale', route: 'sandbox-sales', nav: 'sales' },
  { key: 'balance', route: 'sandbox-balances', nav: 'balances' },
] as const

export type SandboxTutorialStep = (typeof sandboxTutorialSteps)[number]

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
