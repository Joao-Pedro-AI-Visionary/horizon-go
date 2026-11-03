<script setup lang="ts" generic="T extends string | number">
import { nextTick, onMounted, onUnmounted, ref, watch } from 'vue'

// Segmented control with a pill that slides under the pressed option
// (transform + width, var(--dur-seg) ease-out). Options are toggle buttons
// (aria-pressed) in a labelled group; arrow keys move and select, Home/End
// jump to the ends. modelValue may match no option (e.g. a custom date
// range): the pill then fades out.
const props = defineProps<{
  options: readonly { v: T; l: string }[]
  modelValue: T | undefined
  label?: string
  labelledby?: string
  testid: string
  block?: boolean
}>()
const emit = defineEmits<{ 'update:modelValue': [v: T] }>()

const root = ref<HTMLElement | null>(null)
const pill = ref({ x: 0, w: 0, visible: false })
const ready = ref(false)

function measure() {
  const el = root.value
  if (!el) return
  const i = props.options.findIndex((o) => o.v === props.modelValue)
  const btn = i >= 0 ? (el.querySelectorAll<HTMLElement>('.seg-btn')[i] ?? null) : null
  if (!btn) {
    pill.value = { ...pill.value, visible: false }
    return
  }
  pill.value = { x: btn.offsetLeft, w: btn.offsetWidth, visible: true }
}

let ro: ResizeObserver | undefined
onMounted(() => {
  measure()
  // Enable the transition only after the first paint, so the pill does not
  // slide in from the left edge on load.
  requestAnimationFrame(() => requestAnimationFrame(() => (ready.value = true)))
  if (typeof ResizeObserver !== 'undefined' && root.value) {
    ro = new ResizeObserver(measure)
    ro.observe(root.value)
  }
  document.fonts?.ready.then(measure)
})
onUnmounted(() => ro?.disconnect())
watch(
  () => [props.modelValue, props.options],
  () => nextTick(measure),
)

function select(v: T) {
  if (v !== props.modelValue) emit('update:modelValue', v)
}

function onKey(e: KeyboardEvent, i: number) {
  const n = props.options.length
  const to = { ArrowRight: i + 1, ArrowDown: i + 1, ArrowLeft: i - 1, ArrowUp: i - 1, Home: 0, End: n - 1 }[e.key]
  if (to === undefined) return
  e.preventDefault()
  const j = (to + n) % n
  select(props.options[j]!.v)
  root.value?.querySelectorAll<HTMLElement>('.seg-btn')[j]?.focus()
}
</script>

<template>
  <div ref="root" class="seg" :class="block ? 'w-full lg:w-auto' : ''" role="group" :aria-label="label" :aria-labelledby="labelledby" :data-testid="testid">
    <span
      class="seg-pill"
      :class="ready ? 'is-ready' : ''"
      :style="{ transform: `translateX(${pill.x}px)`, width: `${pill.w}px`, opacity: pill.visible ? 1 : 0 }"
      aria-hidden="true"
      :data-testid="`${testid}-pill`"
    />
    <button
      v-for="(o, i) in options"
      :key="String(o.v)"
      type="button"
      class="seg-btn"
      :aria-pressed="modelValue === o.v"
      :data-testid="`${testid}-${o.v}`"
      @click="select(o.v)"
      @keydown="onKey($event, i)"
    >
      {{ o.l }}
    </button>
  </div>
</template>
