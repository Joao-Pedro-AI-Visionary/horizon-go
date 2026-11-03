<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'

// Grouped vertical bar chart in plain SVG: thin bars with a 2px gap,
// 4px rounded data-ends on the baseline, recessive grid, legend for >= 2
// series, hover/focus tooltip per group and a table view for screen readers.
export interface Series {
  name: string
  color: string
  values: number[]
}
const props = defineProps<{
  labels: string[]
  tooltipLabels?: string[]
  series: Series[]
  title: string
  format?: (n: number) => string
  height?: number
  testid?: string
}>()

const fmt = (n: number) => (props.format ? props.format(n) : String(n))
const box = ref<HTMLDivElement>()
const width = ref(600)
let ro: ResizeObserver | undefined
onMounted(() => {
  if (!box.value) return
  width.value = box.value.clientWidth || 600
  ro = new ResizeObserver(([e]) => (width.value = Math.max(240, Math.floor(e!.contentRect.width))))
  ro.observe(box.value)
})
onUnmounted(() => ro?.disconnect())

const H = computed(() => props.height ?? 220)
const maxLabelChars = computed(() => Math.max(...ticks.value.map((t) => fmt(t).length)))
// Left padding fits the widest y tick (10px mono ≈ 6.2px per char).
const pad = computed(() => ({ top: 12, right: 8, bottom: 26, left: Math.ceil(maxLabelChars.value * 6.2) + 14 }))
const plotW = computed(() => width.value - pad.value.left - pad.value.right)
const plotH = computed(() => H.value - pad.value.top - pad.value.bottom)

function niceMax(v: number): number {
  if (v <= 0) return 4
  const p = 10 ** Math.floor(Math.log10(v))
  const n = v / p
  const step = n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10
  return step * p
}
const max = computed(() => niceMax(Math.max(0, ...props.series.flatMap((s) => s.values))))
const ticks = computed(() => [0, 0.25, 0.5, 0.75, 1].map((f) => f * max.value))
const groupW = computed(() => plotW.value / Math.max(1, props.labels.length))
const barW = computed(() => {
  const n = props.series.length
  return Math.max(2, Math.min(28, (groupW.value * 0.72 - (n - 1) * 2) / n))
})
const labelEvery = computed(() => Math.max(1, Math.ceil(props.labels.length / Math.max(1, Math.floor(plotW.value / 46)))))
const y = (v: number) => pad.value.top + plotH.value - (v / max.value) * plotH.value

function barPath(x: number, v: number): string {
  const h = (v / max.value) * plotH.value
  if (h <= 0) return ''
  const r = Math.min(4, barW.value / 2, h)
  const top = pad.value.top + plotH.value - h
  const b = pad.value.top + plotH.value
  const w = barW.value
  return `M${x},${b}V${top + r}Q${x},${top} ${x + r},${top}H${x + w - r}Q${x + w},${top} ${x + w},${top + r}V${b}Z`
}
const groupX = (i: number) => pad.value.left + i * groupW.value
const barX = (i: number, j: number) => {
  const n = props.series.length
  const total = n * barW.value + (n - 1) * 2
  return groupX(i) + (groupW.value - total) / 2 + j * (barW.value + 2)
}

// Re-key bars when the data changes so the grow-in replays.
const sig = computed(() => props.series.map((s) => s.values.join(',')).join('|'))

const active = ref<number | null>(null)
const tip = computed(() => {
  if (active.value === null) return null
  const i = active.value
  const x = groupX(i) + groupW.value / 2
  return { i, left: Math.min(Math.max(x, 80), width.value - 80) }
})
</script>

<template>
  <figure class="min-w-0" :data-testid="testid">
    <figcaption class="sr-only">{{ title }}</figcaption>
    <ul v-if="series.length > 1" class="mb-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted" aria-hidden="true">
      <li v-for="s in series" :key="s.name" class="flex items-center gap-1.5">
        <span class="size-2.5 rounded-sm" :style="{ background: s.color }" />{{ s.name }}
      </li>
    </ul>
    <div ref="box" class="relative" @mouseleave="active = null">
      <svg :width="width" :height="H" :viewBox="`0 0 ${width} ${H}`" role="img" :aria-label="title" class="block max-w-full">
        <g aria-hidden="true">
          <g v-for="t in ticks" :key="t">
            <line :x1="pad.left" :x2="width - pad.right" :y1="y(t)" :y2="y(t)" stroke="#2a2a2a" :stroke-dasharray="t === 0 ? undefined : '2 4'" />
            <text :x="pad.left - 8" :y="y(t) + 4" text-anchor="end" class="fill-muted font-mono text-[10px]">{{ fmt(t) }}</text>
          </g>
          <template v-for="(l, i) in labels" :key="`${l}-${sig}`">
            <rect v-if="active === i" :x="groupX(i)" :y="pad.top" :width="groupW" :height="plotH" fill="#ffffff" fill-opacity="0.05" />
            <path v-for="(s, j) in series" :key="s.name" :d="barPath(barX(i, j), s.values[i] ?? 0)" :fill="s.color" class="bar-grow" :style="{ '--d': `${Math.min(i, 30) * 22 + j * 60}ms` }" />
            <text v-if="(labels.length - 1 - i) % labelEvery === 0" :x="groupX(i) + groupW / 2" :y="H - 8" text-anchor="middle" class="fill-muted font-mono text-[10px]">{{ l }}</text>
          </template>
        </g>
        <!-- Hit targets: full-height per group, keyboard focusable. -->
        <rect
          v-for="(l, i) in labels"
          :key="`hit-${l}`"
          :x="groupX(i)"
          :y="pad.top"
          :width="groupW"
          :height="plotH"
          fill="transparent"
          tabindex="0"
          :aria-label="`${tooltipLabels?.[i] ?? l}: ${series.map((s) => `${s.name} ${fmt(s.values[i] ?? 0)}`).join(', ')}`"
          @mouseenter="active = i"
          @focus="active = i"
          @blur="active = null"
        />
      </svg>
      <div
        v-if="tip"
        class="pointer-events-none absolute top-0 z-10 -translate-x-1/2 rounded-md border border-line bg-ink/95 px-3 py-2 text-xs shadow-xl"
        :style="{ left: `${tip.left}px` }"
        role="presentation"
      >
        <p class="mb-1 font-semibold text-white">{{ tooltipLabels?.[tip.i] ?? labels[tip.i] }}</p>
        <p v-for="s in series" :key="s.name" class="flex items-center gap-2 whitespace-nowrap text-white/85">
          <span class="size-2 rounded-sm" :style="{ background: s.color }" />{{ s.name }}
          <span class="ml-auto pl-3 font-mono text-white">{{ fmt(s.values[tip.i] ?? 0) }}</span>
        </p>
      </div>
    </div>
    <details class="mt-2 text-xs text-muted">
      <summary class="inline-flex min-h-11 cursor-pointer items-center font-mono tracking-wider uppercase hover:text-white" data-testid="chart-table-toggle">Ver tabela</summary>
      <div class="mt-2 max-h-56 overflow-auto">
        <table class="w-full text-left">
          <thead>
            <tr>
              <th scope="col" class="py-1 pr-3 font-medium">Dia</th>
              <th v-for="s in series" :key="s.name" scope="col" class="py-1 pr-3 text-right font-medium">{{ s.name }}</th>
            </tr>
          </thead>
          <tbody class="font-mono text-white/85">
            <tr v-for="(l, i) in labels" :key="l" class="border-t border-line">
              <th scope="row" class="py-1 pr-3 font-normal">{{ tooltipLabels?.[i] ?? l }}</th>
              <td v-for="s in series" :key="s.name" class="py-1 pr-3 text-right">{{ fmt(s.values[i] ?? 0) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </details>
  </figure>
</template>
