import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import AppDialog from './AppDialog.vue'

describe('AppDialog', () => {
  let showModal: ReturnType<typeof vi.spyOn>

  beforeEach(() => {
    showModal = vi.spyOn(HTMLDialogElement.prototype, 'showModal').mockImplementation(function () {
      this.setAttribute('open', '')
    })
  })

  afterEach(() => {
    showModal.mockRestore()
    document.body.innerHTML = ''
  })

  it('opens in the native top layer, handles Escape and restores focus', async () => {
    const trigger = document.createElement('button')
    document.body.append(trigger)
    trigger.focus()

    const wrapper = mount(AppDialog, {
      attachTo: document.body,
      props: { label: 'Sicher löschen?' },
      slots: { default: '<button autofocus>Abbrechen</button>' },
    })
    await flushPromises()

    expect(showModal).toHaveBeenCalledOnce()
    const labelledBy = wrapper.get('dialog').attributes('aria-labelledby')
    expect(wrapper.get(`#${labelledBy}`).text()).toBe('Sicher löschen?')
    expect(document.activeElement?.textContent).toBe('Abbrechen')

    await wrapper.get('dialog').trigger('cancel')
    expect(wrapper.emitted('close')).toHaveLength(1)

    wrapper.unmount()
    await new Promise((resolve) => queueMicrotask(resolve))
    expect(document.activeElement).toBe(trigger)
  })

  it('cannot be dismissed while a blocking action is running', async () => {
    const wrapper = mount(AppDialog, {
      props: { label: 'Wird gespeichert', dismissible: false },
    })
    await wrapper.get('dialog').trigger('cancel')
    expect(wrapper.emitted('close')).toBeUndefined()
  })
})
