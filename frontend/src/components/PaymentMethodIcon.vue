<script setup lang="ts">
import { computed } from 'vue'

/**
 * The glyph for a payment method.
 *
 * Inline SVG on purpose: the till has to work at a gig with no connection, so
 * nothing here may depend on a font or a file being fetched. Everything is
 * drawn in currentColor and inherits the chip's state.
 *
 * The methods come from the server (`models.PaymentMethods`), so an unknown one
 * is a normal state rather than a bug — it falls back to a neutral mark instead
 * of leaving a hole in the row.
 *
 * PayPal is the one real brand mark. Naming a payment option by its logo is
 * what the mark is for, and a customer at the stand recognises it faster than
 * any drawing of a phone. The outline is the official one (path data from
 * simple-icons, CC0; the trademark itself stays PayPal's), so it is filled
 * rather than stroked like the others. Every other method is described, not
 * branded — a bank transfer has no owner.
 */
const props = defineProps<{ method: string }>()

type Glyph = 'cash' | 'paypal' | 'bank' | 'card' | 'other'

const glyph = computed<Glyph>(() => {
  const name = props.method.toLowerCase()
  if (name.includes('bar')) return 'cash'
  if (name.includes('paypal')) return 'paypal'
  if (name.includes('überweis') || name.includes('uberweis') || name.includes('transfer')) {
    return 'bank'
  }
  if (name.includes('karte') || name.includes('card')) return 'card'
  return 'other'
})
</script>

<template>
  <svg
    class="payment-icon"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="1.7"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
    focusable="false"
  >
    <!-- Banknote -->
    <template v-if="glyph === 'cash'">
      <rect x="2.5" y="6" width="19" height="12" rx="2.5" />
      <circle cx="12" cy="12" r="2.6" />
      <path d="M6 9.5v5M18 9.5v5" />
    </template>

    <!-- PayPal's own mark -->
    <template v-else-if="glyph === 'paypal'">
      <!-- The mark fills its viewBox edge to edge while the stroked glyphs sit
           inset, so it is scaled down to match them optically. -->
      <path
        fill="currentColor"
        stroke="none"
        transform="translate(1.44 1.44) scale(0.88)"
        d="M15.607 4.653H8.941L6.645 19.251H1.82L4.862 0h7.995c3.754 0 6.375 2.294 6.473 5.513-.648-.478-2.105-.86-3.722-.86m6.57 5.546c0 3.41-3.01 6.853-6.958 6.853h-2.493L11.595 24H6.74l1.845-11.538h3.592c4.208 0 7.346-3.634 7.153-6.949a5.24 5.24 0 0 1 2.848 4.686M9.653 5.546h6.408c.907 0 1.942.222 2.363.541-.195 2.741-2.655 5.483-6.441 5.483H8.714Z"
      />
    </template>

    <!-- Bank transfer -->
    <template v-else-if="glyph === 'bank'">
      <path d="M3.5 9.5 12 4.5l8.5 5" />
      <path d="M5.5 9.5v8M10 9.5v8M14 9.5v8M18.5 9.5v8" />
      <path d="M3 20h18" />
    </template>

    <!-- Card -->
    <template v-else-if="glyph === 'card'">
      <rect x="2.5" y="5" width="19" height="14" rx="2.5" />
      <path d="M2.5 10h19" />
      <path d="M6 15h4" />
    </template>

    <!-- Anything the server sends that has no glyph of its own -->
    <template v-else>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M8.5 12h.01M12 12h.01M15.5 12h.01" />
    </template>
  </svg>
</template>

<style scoped>
.payment-icon {
  width: 22px;
  height: 22px;
  flex: 0 0 auto;
}
</style>
