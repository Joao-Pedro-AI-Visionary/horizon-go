<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import type { Series } from './BarChart.vue'

// Line + area chart in plain SVG for daily series. The first series gets a
// gradient area; lines trace themselves in (line-draw) and the area fades in.
// Hover/focus tooltip per day and a table view, like BarChart.
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

const uid = `ac-${Math.random().toString(36).slice(2, 8)}`
const H = computed(() => props.height ?? 260)
function niceMax(v: number): number {
  if (v <= 0) return 4
  const p = 10 ** Math.floor(Math.log10(v))
  const n = v / p
  return (n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10) * p
}
const max = computed(() => niceMax(Math.max(0, ...props.series.flatMap((s) => s.values))))
const ticks = computed(() => [0, 0.25, 0.5, 0.75, 1].map((f) => f * max.value))
const pad = computed(() => ({ top: 14, right: 12, bottom: 26, left: Math.ceil(Math.max(...ticks.value.map((t) => fmt(t).length)) * 6.2) + 14 }))
const plotW = computed(() => width.value - pad.value.left - pad.value.right)
const plotH = computed(() => H.value - pad.value.top - pad.value.bottom)
const n = computed(() => props.labels.length)
const x = (i: number) => pad.value.left + (n.value <= 1 ? plotW.value / 2 : (i / (n.value - 1)) * plotW.value)
const y = (v: number) => pad.value.top + plotH.value - (v / max.value) * plotH.value
const linePath = (vals: number[]) => vals.map((v, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)},${y(v).toFixed(1)}`).join('')
const areaPath = (vals: number[]) => (vals.length ? `${linePath(vals)}L${x(vals.length - 1)},${y(0)}L${x(0)},${y(0)}Z` : '')
const labelEvery = computed(() => Math.max(1, Math.ceil(n.value / Math.max(1, Math.floor(plotW.value / 46)))))
// Re-key the drawing when the data changes so the draw-in replays.
const sig = computed(() => props.series.map((s) => s.values.join(',')).join('|'))

const active = ref<number | null>(null)
const colW = computed(() => plotW.value / Math.max(1, n.value))
const tip = computed(() => (active.value === null ? null : { i: active.value, left: Math.min(Math.max(x(active.value), 80), width.value - 80) }))
</script>

<template>
  <figure class="min-w-0" :data-testid="testid">
    <figcaption class="sr-only">{{ title }}</figcaption>
    <ul v-if="series.length > 1" class="mb-3 flex flex-wrap gap-x-5 gap-y-1 font-mono text-xs text-muted" aria-hidden="true">
      <li v-for="s in series" :key="s.name" class="flex items-center gap-1.5"><span class="h-0.5 w-4" :style="{ background: s.color }" />{{ s.name }}</li>
    </ul>
    <div ref="box" class="relative" @mouseleave="active = null">
      <svg :width="width" :height="H" :viewBox="`0 0 ${width} ${H}`" role="img" :aria-label="title" class="block max-w-full">
        <defs>
          <linearGradient :id="`${uid}-g`" x1="0" x2="0" y1="0" y2="1">
            <stop offset="0%" :stop-color="series[0]?.color" stop-opacity="0.32" />
            <stop offset="100%" :stop-color="series[0]?.color" stop-opacity="0" />
          </linearGradient>
        </defs>
        <g aria-hidden="true">
          <g v-for="t in ticks" :key="t">
            <line :x1="pad.left" :x2="width - pad.right" :y1="y(t)" :y2="y(t)" stroke="#2a2a2a" :stroke-dasharray="t === 0 ? undefined : '2 4'" />
            <text :x="pad.left - 8" :y="y(t) + 4" text-anchor="end" class="fill-muted font-mono text-[10px]">{{ fmt(t) }}</text>
          </g>
          <template v-for="(l, i) in labels" :key="l">
            <text v-if="(labels.length - 1 - i) % labelEvery === 0" :x="x(i)" :y="H - 8" text-anchor="middle" class="fill-muted font-mono text-[10px]">{{ l }}</text>
          </template>
          <g :key="sig">
            <path v-if="series[0]" :d="areaPath(series[0].values)" :fill="`url(#${uid}-g)`" class="fade-in" style="--d: 300ms" />
            <path
              v-for="(s, j) in series"
              :key="s.name"
              :d="linePath(s.values)"
              fill="none"
              :stroke="s.color"
              stroke-width="2"
              stroke-linejoin="round"
              stroke-linecap="round"
              pathLength="1"
              class="line-draw"
              :style="{ '--d': `${j * 180}ms` }"
            />
          </g>
          <template v-if="active !== null">
            <line :x1="x(active)" :x2="x(active)" :y1="pad.top" :y2="pad.top + plotH" stroke="#ffffff" stroke-opacity="0.25" />
            <circle v-for="s in series" :key="s.name" :cx="x(active)" :cy="y(s.values[active] ?? 0)" r="4" :fill="s.color" stroke="#0b0b0b" stroke-width="2" />
          </template>
        </g>
        <rect
          v-for="(l, i) in labels"
          :key="`hit-${l}`"
          :x="x(i) - colW / 2"
          :y="pad.top"
          :width="colW"
          :height="plotH"
          fill="transparent"
          tabindex="0"
          :aria-label="`${tooltipLabels?.[i] ?? l}: ${series.map((s) => `${s.name} ${fmt(s.values[i] ?? 0)}`).join(', ')}`"
          @mouseenter="active = i"
          @focus="active = i"
          @blur="active = null"
        />
      </svg>
      <div v-if="tip" class="pointer-events-none absolute top-0 z-10 -translate-x-1/2 rounded-md border border-line bg-ink/95 px-3 py-2 text-xs shadow-xl" :style="{ left: `${tip.left}px` }" role="presentation">
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
