import { defineComponent, ref } from 'vue'
import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { usePendingChangesGuard } from './usePendingChangesGuard'

const { captured } = vi.hoisted(() => ({
  captured: { guard: null as null | (() => boolean) },
}))

vi.mock('vue-router', () => ({
  onBeforeRouteLeave: (guard: () => boolean) => { captured.guard = guard },
}))

describe('usePendingChangesGuard', () => {
  beforeEach(() => {
    captured.guard = null
    vi.restoreAllMocks()
  })

  it('protects route changes and browser unloads only while work is pending', () => {
    const pending = ref(true)
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    const wrapper = mount(defineComponent({
      setup() {
        usePendingChangesGuard(pending, () => 'unfinished')
        return () => null
      },
    }))

    expect(captured.guard?.()).toBe(false)
    expect(confirm).toHaveBeenCalledWith('unfinished')

    const unload = new Event('beforeunload', { cancelable: true }) as BeforeUnloadEvent
    const preventDefault = vi.spyOn(unload, 'preventDefault')
    window.dispatchEvent(unload)
    expect(preventDefault).toHaveBeenCalledOnce()

    pending.value = false
    confirm.mockClear()
    expect(captured.guard?.()).toBe(true)
    expect(confirm).not.toHaveBeenCalled()

    const safeUnload = new Event('beforeunload', { cancelable: true }) as BeforeUnloadEvent
    const safePreventDefault = vi.spyOn(safeUnload, 'preventDefault')
    window.dispatchEvent(safeUnload)
    expect(safePreventDefault).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
