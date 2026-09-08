import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import NotFoundView from './NotFoundView.vue'

describe('NotFoundView accessibility target', () => {
  it('provides the skip-link destination on its main content', () => {
    const wrapper = mount(NotFoundView)

    expect(wrapper.get('main').attributes()).toMatchObject({ id: 'main-content', tabindex: '-1' })
  })
})
