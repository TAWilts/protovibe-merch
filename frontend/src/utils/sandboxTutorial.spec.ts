import { describe, expect, it } from 'vitest'

import type { SandboxIdentity } from '@/api/types'
import {
  nextSandboxTutorialStep,
  sandboxExplorationSteps,
  sandboxTutorialComplete,
} from './sandboxTutorial'

function sandbox(progress: SandboxIdentity['tutorial_state'], visible = true): SandboxIdentity {
  return {
    id: 1,
    expires_at: '2026-09-12T00:00:00Z',
    demo_role: 'band_admin',
    template_version: 2,
    tutorial_state: progress,
    tutorial_visible: visible,
    storage_used_bytes: 0,
    storage_quota_bytes: 25 * 1024 * 1024,
  }
}

describe('nextSandboxTutorialStep', () => {
  it('returns the first incomplete task in the fixed learning sequence', () => {
    expect(nextSandboxTutorialStep(sandbox({ catalogue: false, purchase: false, sale: false, balance: false }))?.key).toBe('catalogue')
    expect(nextSandboxTutorialStep(sandbox({ catalogue: true, purchase: false, sale: false, balance: false }))?.key).toBe('purchase')
    expect(nextSandboxTutorialStep(sandbox({ catalogue: true, purchase: true, sale: false, balance: false }))?.key).toBe('sale')
    expect(nextSandboxTutorialStep(sandbox({ catalogue: true, purchase: true, sale: true, balance: false }))?.key).toBe('balance')
  })

  it('returns no task after completion or when the tutorial was skipped', () => {
    expect(nextSandboxTutorialStep(sandbox({ catalogue: true, purchase: true, sale: true, balance: true }))).toBeNull()
    expect(nextSandboxTutorialStep(sandbox({ catalogue: false, purchase: false, sale: false, balance: false }, false))).toBeNull()
  })

  it('recognises the transition to the optional exploration journey', () => {
    expect(sandboxTutorialComplete(sandbox({ catalogue: true, purchase: true, sale: true, balance: false }))).toBe(false)
    expect(sandboxTutorialComplete(sandbox({ catalogue: true, purchase: true, sale: true, balance: true }))).toBe(true)
    expect(sandboxExplorationSteps.map((step) => step.key)).toEqual([
      'shipping',
      'cancellation',
      'slideshow',
      'packing',
      'roles',
    ])
  })
})
