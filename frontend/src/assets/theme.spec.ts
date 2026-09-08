import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const themeCss = readFileSync(resolve(process.cwd(), 'src/assets/theme.css'), 'utf8')
const baseCss = readFileSync(resolve(process.cwd(), 'src/assets/base.css'), 'utf8')

const semanticTokens = [
  '--surface-page',
  '--surface-panel',
  '--surface-raised',
  '--surface-subtle',
  '--surface-inset',
  '--surface-hover',
  '--surface-pressed',
  '--surface-selected',
  '--border-subtle',
  '--border-default',
  '--border-strong',
  '--text-primary',
  '--text-secondary',
  '--text-tertiary',
  '--radius-small',
  '--radius-control',
  '--radius-panel',
  '--radius-overlay',
  '--shadow-sm',
  '--shadow-md',
  '--shadow-overlay',
  '--success-text',
  '--success-border',
  '--success-soft',
  '--warning-text',
  '--warning-border',
  '--warning-soft',
  '--danger-text',
  '--danger-border',
  '--danger-soft',
]

const themePalettes = {
  ':root': ['#101114', '#181a1f', '#20232a', '#3b3f48', '#f4f4f6', '#d56cdb'],
  'html[data-theme="ocean"]': ['#0d161b', '#142229', '#1b2e36', '#31515c', '#eef8fb', '#55bed0'],
  'html[data-theme="sunset"]': ['#1b1316', '#271a1f', '#322127', '#5d3e49', '#fff4f1', '#df6c91'],
  'html[data-theme="forest"]': ['#0e1814', '#16251f', '#1c3027', '#365848', '#eff8f2', '#61bd89'],
  'html[data-theme="midnight"]': ['#111621', '#192131', '#202a3e', '#3e4c6a', '#f1f4ff', '#899cec'],
}

describe('theme token contract', () => {
  it('defines every semantic token and compatibility alias', () => {
    for (const token of semanticTokens) expect(themeCss).toContain(`${token}:`)
    for (const alias of ['--panel', '--text', '--radius', '--shadow']) {
      expect(themeCss).toContain(`${alias}: var(`)
    }
  })

  it.each(Object.entries(themePalettes))('keeps the agreed core palette for %s', (selector, colors) => {
    const start = themeCss.indexOf(`${selector} {`)
    const end = themeCss.indexOf('}', start)
    const block = themeCss.slice(start, end)

    expect(start).toBeGreaterThanOrEqual(0)
    for (const color of colors) expect(block).toContain(color)
  })

  it('does not use the retired undefined surface token', () => {
    expect(baseCss).not.toContain('var(--surface-muted)')
    expect(baseCss).not.toContain('var(--surface)')
  })
})
