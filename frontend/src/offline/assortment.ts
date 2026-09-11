import type { Article } from '@/api/types'
import { offlineDB, type SalesAssortmentRecord } from './database'

export interface SalesAssortment {
  articles: Article[]
  payment_methods: string[]
}

/** Keeps only the latest confirmed server snapshot for each band. */
export async function saveSalesAssortment(
  bandId: number,
  assortment: SalesAssortment,
): Promise<SalesAssortmentRecord> {
  const record: SalesAssortmentRecord = {
    bandId,
    savedAt: new Date().toISOString(),
    articles: assortment.articles,
    paymentMethods: assortment.payment_methods,
  }
  await (await offlineDB()).put('sales_assortments', record)
  return record
}

/** Loads only the calling band's snapshot, never another tenant's data. */
export async function loadSalesAssortment(bandId: number): Promise<SalesAssortmentRecord | null> {
  return await (await offlineDB()).get('sales_assortments', bandId) ?? null
}
