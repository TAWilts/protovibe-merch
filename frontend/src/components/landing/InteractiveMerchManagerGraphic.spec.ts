import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import InteractiveMerchManagerGraphic from './InteractiveMerchManagerGraphic.vue'

const locale = { value: 'de' }

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    locale,
    t: (key: string) => key,
  }),
}))

class ResizeObserverStub {
  observe = vi.fn()
  disconnect = vi.fn()
}

function rect(left: number, top: number, width: number, height: number): DOMRect {
  return {
    left,
    top,
    width,
    height,
    right: left + width,
    bottom: top + height,
    x: left,
    y: top,
    toJSON: () => ({}),
  }
}

describe('InteractiveMerchManagerGraphic', () => {
  beforeEach(() => {
    vi.stubGlobal('ResizeObserver', ResizeObserverStub)
  })

  it('uses the seven supplied image layers and starts with sales selected', () => {
    const wrapper = mount(InteractiveMerchManagerGraphic)
    const images = wrapper.findAll('.art-layer')

    expect(images).toHaveLength(7)
    expect(new Set(images.map((image) => image.attributes('src'))).size).toBe(7)
    expect(wrapper.findAll('.interactive-hotspot')).toHaveLength(6)
    expect(wrapper.get('[data-hotspot="C"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.get('[data-layer="C"]').classes()).toContain('is-highlighted')
  })

  it('embeds the same graphic without duplicating the landing-page heading or anchor', () => {
    const wrapper = mount(InteractiveMerchManagerGraphic, { props: { embedded: true } })

    expect(wrapper.get('.interactive-merch-section').classes()).toContain('is-embedded')
    expect(wrapper.get('.interactive-merch-section').attributes('id')).toBeUndefined()
    expect(wrapper.find('.interactive-heading').exists()).toBe(false)
    expect(wrapper.findAll('.art-layer')).toHaveLength(7)
  })

  it('previews on hover and keeps a clicked area active afterwards', async () => {
    const wrapper = mount(InteractiveMerchManagerGraphic)

    await wrapper.get('[data-hotspot="F"]').trigger('mouseenter')
    expect(wrapper.get('[data-layer="F"]').classes()).toContain('is-highlighted')
    expect(wrapper.findAll('[data-layer].is-highlighted')).toHaveLength(1)

    await wrapper.get('[data-hotspot="F"]').trigger('mouseleave')
    expect(wrapper.get('[data-layer="C"]').classes()).toContain('is-highlighted')

    await wrapper.get('[data-hotspot="A"]').trigger('click')
    expect(wrapper.get('[data-hotspot="A"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.get('[data-layer="A"]').classes()).toContain('is-highlighted')
  })

  it('keeps offline and slideshow as separate overlapping controls', async () => {
    const wrapper = mount(InteractiveMerchManagerGraphic)
    const offline = wrapper.get('[data-hotspot="D"]')
    const slideshow = wrapper.get('[data-hotspot="E"]')
    const sales = wrapper.get('[data-hotspot="C"]')

    expect(offline.attributes('style')).toContain('z-index: 6')
    expect(slideshow.attributes('style')).toContain('z-index: 5')
    expect(sales.attributes('style')).toContain('z-index: 4')

    await offline.trigger('click')
    expect(wrapper.get('[data-layer="D"]').classes()).toContain('is-highlighted')
    expect(wrapper.get('[data-layer="E"]').classes()).not.toContain('is-highlighted')

    await slideshow.trigger('click')
    expect(wrapper.get('[data-layer="E"]').classes()).toContain('is-highlighted')
    expect(wrapper.get('[data-layer="D"]').classes()).not.toContain('is-highlighted')
  })

  it('draws one straight segment when aligned and otherwise exactly two segments', async () => {
    const wrapper = mount(InteractiveMerchManagerGraphic)
    const canvas = wrapper.get('.interactive-canvas').element as HTMLElement
    const callout = wrapper.get('.interactive-callout').element as HTMLElement
    canvas.getBoundingClientRect = () => rect(0, 0, 1774, 887)
    callout.getBoundingClientRect = () => rect(300, 50, 400, 200)

    window.dispatchEvent(new Event('resize'))
    await flushPromises()
    expect(wrapper.get('[data-connector]').attributes('d')).toBe('M706 320 V150 H500')

    await wrapper.get('[data-hotspot="A"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-connector]').attributes('d')).toBe('M300 391 V150')
  })
})
