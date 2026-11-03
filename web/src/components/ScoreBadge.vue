<script setup lang="ts">
import { computed } from 'vue'
import { fmtScore } from '../lib/format'

// The LLM score (0–10) as an editorial number with a 10-step meter below.
// Yellow when it reaches the digest threshold, white otherwise.
const props = defineProps<{ score: number | null; threshold?: number; size?: 'sm' | 'md' | 'xl'; delay?: string }>()
const strong = computed(() => props.score !== null && props.score >= (props.threshold ?? 6))
const filled = computed(() => Math.round(props.score ?? 0))
const numClass = computed(
  () => ({ sm: 'text-2xl', md: 'text-4xl', xl: 'text-[clamp(4.5rem,9vw,7.5rem)]' })[props.size ?? 'md'],
)
</script>

<template>
  <div class="flex shrink-0 flex-col" role="img" :aria-label="score === null ? 'Sem nota' : `Nota ${fmtScore(score)} de 10`">
    <span class="num leading-[0.85] font-bold" :class="[numClass, strong ? 'text-primary' : 'text-white']" aria-hidden="true">{{ fmtScore(score) }}</span>
    <span class="mt-2 flex gap-[3px]" :class="size === 'xl' ? 'w-56 max-w-full' : 'w-16'" aria-hidden="true">
      <span
        v-for="i in 10"
        :key="i"
        class="h-1 flex-1 rounded-[1px]"
        :class="i <= filled ? (strong ? 'meter-fill bg-primary' : 'meter-fill bg-white/80') : 'bg-white/12'"
        :style="{ '--d': `calc(${delay ?? '0ms'} + ${i * 35}ms)` }"
      />
    </span>
  </div>
</template>
