import 'fake-indexeddb/auto'
import { describe, expect, it } from 'vitest'

import { loadSalesAssortment, saveSalesAssortment } from './assortment'

const assortment = (name: string) => ({
  articles: [{ id: 1, name, option_groups: [], variants: [], total_stock: 0 }],
  payment_methods: ['Bar'],
})

describe('offline sales assortment', () => {
  it('stores the latest successful assortment under its band ID', async () => {
    await saveSalesAssortment(4101, assortment('Erster Stand'))
    await saveSalesAssortment(4101, assortment('Aktueller Stand'))

    expect(await loadSalesAssortment(4101)).toMatchObject({
      bandId: 4101,
      articles: [{ name: 'Aktueller Stand' }],
      paymentMethods: ['Bar'],
    })
  })

  it('never returns another band’s cached assortment', async () => {
    await saveSalesAssortment(4201, assortment('Band Eins'))
    await saveSalesAssortment(4202, assortment('Band Zwei'))

    expect((await loadSalesAssortment(4201))?.articles[0]?.name).toBe('Band Eins')
    expect((await loadSalesAssortment(4202))?.articles[0]?.name).toBe('Band Zwei')
    expect(await loadSalesAssortment(4299)).toBeNull()
  })
})
