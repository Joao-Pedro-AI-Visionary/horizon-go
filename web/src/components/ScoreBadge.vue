<script setup lang="ts">
import { computed } from 'vue'
import { fmtScore } from '../lib/format'

// The LLM score (0–10) is the hero of each card: yellow when it reaches the
// digest threshold, outlined otherwise.
const props = defineProps<{ score: number | null; threshold?: number; size?: 'md' | 'lg' }>()
const strong = computed(() => props.score !== null && props.score >= (props.threshold ?? 6))
</script>

<template>
  <div
    class="flex shrink-0 flex-col items-center justify-center rounded-xl font-mono leading-none"
    :class="[
      size === 'lg' ? 'size-20' : 'size-14',
      strong ? 'bg-primary text-ink' : 'border border-primary/40 text-primary',
    ]"
    :aria-label="score === null ? 'Sem nota' : `Nota ${fmtScore(score)} de 10`"
    role="img"
  >
    <span class="font-bold" :class="size === 'lg' ? 'text-3xl' : 'text-xl'">{{ fmtScore(score) }}</span>
    <span class="mt-0.5 text-[10px] opacity-80" aria-hidden="true">/10</span>
  </div>
</template>
