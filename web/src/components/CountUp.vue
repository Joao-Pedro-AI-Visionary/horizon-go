<script setup lang="ts">
import { computed, toRef } from 'vue'
import { useCountUp } from '../lib/motion'

// Animated number. Screen readers and tests read the final value
// (sr-only text and data-value); the counting digits are decorative.
const props = defineProps<{ value: number; format?: (n: number) => string; decimals?: number }>()
const shown = useCountUp(toRef(props, 'value'))
const fmt = (n: number) => {
  const v = props.decimals ? Number(n.toFixed(props.decimals)) : Math.round(n)
  return props.format ? props.format(v) : String(v)
}
const final = computed(() => fmt(props.value))
</script>

<template>
  <span :data-value="value" class="num"><span class="sr-only">{{ final }}</span><span aria-hidden="true">{{ fmt(shown) }}</span></span>
</template>
