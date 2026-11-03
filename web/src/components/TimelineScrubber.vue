<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { api, errorMessage } from '../api/client'
import type { FeedTimeline, TimelineParams } from '../api/types'
import { fmtDay, fmtNumber } from '../lib/format'

// Date scrubber over the real per-day histogram of the feed
// (GET /api/feed/timeline, same non-time filters as the feed). Two handles
// (role=slider) pick a range of calendar days; the label updates live while
// dragging and the range is committed as from/to on release (pointer) or
// 400ms after the last key. Single-pointer alternative to dragging: a tap on
// the track moves the nearest handle there (WCAG 2.5.7).
const props = defineProps<{
  params: TimelineParams
  from?: string
  to?: string
  period?: string
  days?: number
}>()
const emit = defineEmits<{ range: [r: { from: string; to?: string }]; clear: [] }>()

const tl = ref<FeedTimeline | null>(null)
const loading = ref(true)
const error = ref('')
let ctrl: AbortController | undefined
async function load() {
  ctrl?.abort()
  ctrl = new AbortController()
  loading.value = true
  error.value = ''
  try {
    tl.value = await api.feedTimeline({ ...props.params, days: props.days ?? 30 }, ctrl.signal)
  } catch (err) {
    if ((err as Error).name === 'AbortError') return
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}
watch(() => JSON.stringify(props.params), load, { immediate: true })
onUnmounted(() => {
  ctrl?.abort()
  clearTimeout(keyTimer)
})

const buckets = computed(() => tl.value?.buckets ?? [])
const n = computed(() => buckets.value.length)
const maxCount = computed(() => Math.max(1, ...buckets.value.map((b) => b.count)))
const custom = computed(() => !!props.from)

// Index of the last bucket that starts at or before `iso`.
function indexAt(iso: string, minusMs = 0): number {
  const t = Date.parse(iso) - minusMs
  let k = 0
  buckets.value.forEach((b, i) => {
    if (Date.parse(b.start) <= t) k = i
  })
  return k
}

// Range shown for the URL state: the custom range, or the calendar days a
// rolling period touches (24h spans today and yesterday, and so on).
const periodDays: Record<string, number> = { '24h': 2, '7d': 8, '30d': 31 }
const derived = computed<[number, number]>(() => {
  const last = Math.max(0, n.value - 1)
  if (props.from) return [indexAt(props.from), props.to ? indexAt(props.to, 1) : last]
  const d = periodDays[props.period ?? '']
  return [d ? Math.max(0, n.value - d) : 0, last]
})

const lo = ref(0)
const hi = ref(0)
const dragging = ref<null | 'lo' | 'hi' | 'pending'>(null)
watch(
  derived,
  ([a, b]) => {
    if (dragging.value) return
    lo.value = a
    hi.value = b
  },
  { immediate: true },
)

const selCount = computed(() => buckets.value.slice(lo.value, hi.value + 1).reduce((s, b) => s + b.count, 0))
const MONTHS = ['jan', 'fev', 'mar', 'abr', 'mai', 'jun', 'jul', 'ago', 'set', 'out', 'nov', 'dez']
function short(day?: string) {
  if (!day) return '—'
  const [, m, d] = day.split('-')
  return `${d} ${MONTHS[Number(m) - 1]}`
}
const loDay = computed(() => buckets.value[lo.value]?.day)
const hiDay = computed(() => buckets.value[hi.value]?.day)
const valueText = (i: number) => {
  const b = buckets.value[i]
  return b ? `${fmtDay(b.day)}, ${b.count} ${b.count === 1 ? 'item' : 'itens'}` : ''
}

function commit() {
  const b = buckets.value
  if (!b.length) return
  const [a, z] = derived.value
  if (custom.value && a === lo.value && z === hi.value) return
  emit('range', { from: b[lo.value]!.start, to: hi.value >= b.length - 1 ? undefined : b[hi.value + 1]!.start })
}

// --- Pointer ---
const track = ref<HTMLElement | null>(null)
const thumbs = { lo: ref<HTMLElement | null>(null), hi: ref<HTMLElement | null>(null) }
let downIdx = 0
function idxFromX(clientX: number): number {
  const r = track.value!.getBoundingClientRect()
  return Math.min(n.value - 1, Math.max(0, Math.floor(((clientX - r.left) / r.width) * n.value)))
}
function moveTo(which: 'lo' | 'hi', i: number) {
  if (which === 'lo') lo.value = Math.min(i, hi.value)
  else hi.value = Math.max(i, lo.value)
}
function onDown(e: PointerEvent) {
  if (!n.value || e.button !== 0) return
  const i = idxFromX(e.clientX)
  const t = (e.target as HTMLElement).closest<HTMLElement>('[data-thumb]')?.dataset.thumb as 'lo' | 'hi' | undefined
  let which: 'lo' | 'hi' | 'pending'
  if (t && lo.value !== hi.value) which = t
  else if (i < lo.value) which = 'lo'
  else if (i > hi.value) which = 'hi'
  else if (lo.value === hi.value) which = 'pending'
  else which = i - lo.value <= hi.value - i ? 'lo' : 'hi'
  downIdx = i
  dragging.value = which
  if (which !== 'pending') {
    moveTo(which, i)
    thumbs[which].value?.focus({ preventScroll: true })
  }
  track.value!.setPointerCapture(e.pointerId)
  e.preventDefault()
}
function onMove(e: PointerEvent) {
  if (!dragging.value) return
  const i = idxFromX(e.clientX)
  if (dragging.value === 'pending') {
    if (i === downIdx) return
    dragging.value = i < downIdx ? 'lo' : 'hi'
    thumbs[dragging.value].value?.focus({ preventScroll: true })
  }
  moveTo(dragging.value, i)
}
function onUp(e: PointerEvent) {
  if (!dragging.value) return
  dragging.value = null
  track.value?.releasePointerCapture?.(e.pointerId)
  commit()
}

// --- Keyboard (role=slider) ---
let keyTimer: ReturnType<typeof setTimeout> | undefined
function onKey(e: KeyboardEvent, which: 'lo' | 'hi') {
  const cur = which === 'lo' ? lo.value : hi.value
  const last = n.value - 1
  const next = {
    ArrowLeft: cur - 1,
    ArrowDown: cur - 1,
    ArrowRight: cur + 1,
    ArrowUp: cur + 1,
    PageDown: cur - 7,
    PageUp: cur + 7,
    Home: which === 'lo' ? 0 : lo.value,
    End: which === 'hi' ? last : hi.value,
  }[e.key]
  if (next === undefined) return
  e.preventDefault()
  moveTo(which, Math.min(last, Math.max(0, next)))
  clearTimeout(keyTimer)
  keyTimer = setTimeout(commit, 400)
}

const pct = (i: number) => (n.value ? (i / n.value) * 100 : 0)
const axis = computed(() => {
  const b = buckets.value
  return b.length ? [b[0]!.day, b[Math.floor((b.length - 1) / 2)]!.day, b[b.length - 1]!.day] : []
})
</script>

<template>
  <section class="card-float p-4 md:p-5" aria-labelledby="scrub-h" data-testid="scrubber">
    <div class="flex flex-wrap items-end justify-between gap-x-6 gap-y-2">
      <div>
        <h2 id="scrub-h" class="eyebrow">Linha do tempo · {{ tl?.days ?? days ?? 30 }} dias</h2>
        <p class="num mt-1 text-2xl font-bold md:text-3xl" data-testid="scrub-label">
          <span>{{ short(loDay) }}</span><span class="px-2 text-primary" aria-hidden="true">→</span><span class="sr-only"> até </span><span>{{ short(hiDay) }}</span>
        </p>
      </div>
      <p class="num text-sm text-muted">
        <span class="text-xl font-bold text-white" data-testid="scrub-count">{{ fmtNumber(selCount) }}</span>
        {{ selCount === 1 ? 'item' : 'itens' }} nestes dias
      </p>
    </div>

    <div v-if="loading && !tl" class="skeleton mt-4 h-24" aria-hidden="true" data-testid="scrub-loading" />
    <p v-else-if="error" class="mt-4 flex flex-wrap items-center gap-3 text-sm text-danger" role="alert">
      Linha do tempo indisponível: {{ error }}
      <button type="button" class="btn-ghost" @click="load">Tentar de novo</button>
    </p>
    <div v-else class="mt-4" :class="loading ? 'opacity-60' : ''">
      <div
        ref="track"
        class="relative h-24 cursor-pointer touch-pan-y select-none"
        :class="dragging ? 'is-dragging' : ''"
        data-testid="scrub-track"
        @pointerdown="onDown"
        @pointermove="onMove"
        @pointerup="onUp"
        @pointercancel="onUp"
      >
        <!-- Histogram (decorative: the sliders carry the values) -->
        <div class="absolute inset-x-0 top-2 bottom-6 flex items-end gap-[2px]" aria-hidden="true">
          <div
            v-for="(b, i) in buckets"
            :key="b.day"
            class="scrub-bar min-h-[2px] flex-1 rounded-t-[2px]"
            :style="{
              height: `${Math.max(3, (b.count / maxCount) * 100)}%`,
              background: i >= lo && i <= hi ? 'var(--grad-scrub)' : '#3a3a3a',
              opacity: b.count === 0 ? 0.5 : 1,
            }"
            data-testid="scrub-bar"
            :data-count="b.count"
          />
        </div>
        <div
          class="scrub-band pointer-events-none absolute top-0 bottom-6 rounded-md border-x-2 border-primary bg-primary/[0.06]"
          :style="{ left: `${pct(lo)}%`, right: `${100 - pct(hi + 1)}%` }"
          aria-hidden="true"
        />
        <div
          v-for="w in (['lo', 'hi'] as const)"
          :key="w"
          :ref="(el) => (thumbs[w].value = el as HTMLElement | null)"
          class="scrub-thumb group absolute top-0 bottom-6 flex w-11 -translate-x-1/2 cursor-grab items-end justify-center rounded-md outline-offset-0 active:cursor-grabbing"
          :style="{ left: `${pct(w === 'lo' ? lo : hi + 1)}%`, zIndex: w === 'lo' && lo === n - 1 ? 3 : 2 }"
          role="slider"
          tabindex="0"
          :data-thumb="w"
          :aria-label="w === 'lo' ? 'Início da faixa de datas' : 'Fim da faixa de datas'"
          aria-orientation="horizontal"
          :aria-valuemin="w === 'lo' ? 0 : lo"
          :aria-valuemax="w === 'lo' ? hi : n - 1"
          :aria-valuenow="w === 'lo' ? lo : hi"
          :aria-valuetext="valueText(w === 'lo' ? lo : hi)"
          :data-testid="w === 'lo' ? 'scrub-from' : 'scrub-to'"
          @keydown="onKey($event, w)"
        >
          <span class="mb-[-10px] size-5 rounded-full border-[3px] border-ink bg-primary shadow-[0_0_0_1px_var(--color-primary)] transition-transform duration-150 group-hover:scale-110 group-focus-visible:scale-110" aria-hidden="true" />
        </div>
        <div class="absolute inset-x-0 bottom-0 flex justify-between font-mono text-[11px] text-muted" aria-hidden="true">
          <span v-for="d in axis" :key="d">{{ short(d) }}</span>
        </div>
      </div>
      <p v-if="tl && tl.total === 0" class="mt-2 text-xs text-muted">Nenhum item nos últimos {{ tl.days }} dias com esses filtros.</p>
    </div>

    <Transition name="drop">
      <div v-if="custom" class="surface-primary mt-4 flex flex-wrap items-center justify-between gap-3 px-4 py-3" data-testid="scrub-active">
        <p class="text-sm font-semibold">
          <span class="mr-2 font-mono text-xs tracking-[0.14em] uppercase">Faixa ativa</span>
          <span class="num">{{ short(loDay) }} → {{ short(hiDay) }}</span>
        </p>
        <button type="button" class="btn-ink" data-testid="scrub-clear" @click="emit('clear')">Limpar faixa</button>
      </div>
    </Transition>
  </section>
</template>
