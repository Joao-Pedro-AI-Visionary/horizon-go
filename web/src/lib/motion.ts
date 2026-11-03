import { onUnmounted, ref, watch, type Directive, type Ref } from 'vue'

// Motion helpers (see docs/DESIGN.md › Motion). Everything degrades to the
// final state when the user asks for reduced motion.

export const reducedMotion = (): boolean =>
  typeof window !== 'undefined' && window.matchMedia?.('(prefers-reduced-motion: reduce)').matches

const STAGGER_MS = 55
const MAX_STAGGER = 8

/** staggerDelay caps the stagger so long lists never wait more than ~0.45s. */
export const staggerDelay = (i: number) => `${Math.min(Math.max(i, 0), MAX_STAGGER) * STAGGER_MS}ms`

let io: IntersectionObserver | undefined
function observer(): IntersectionObserver | undefined {
  if (io || typeof IntersectionObserver === 'undefined') return io
  io = new IntersectionObserver(
    (entries) => {
      for (const e of entries) {
        if (!e.isIntersecting) continue
        e.target.classList.add('is-in')
        io!.unobserve(e.target)
      }
    },
    { rootMargin: '0px 0px -6% 0px', threshold: 0.01 },
  )
  return io
}

/**
 * v-reveal="index": the element rises and fades in the first time it enters
 * the viewport; `index` staggers siblings. No-op under reduced motion.
 */
export const vReveal: Directive<HTMLElement, number | undefined> = {
  mounted(el, { value }) {
    el.dataset.reveal = ''
    el.style.setProperty('--d', staggerDelay(value ?? 0))
    const o = observer()
    if (!o || reducedMotion()) el.classList.add('is-in')
    else o.observe(el)
  },
  unmounted(el) {
    io?.unobserve(el)
  },
}

/**
 * useCountUp animates a displayed number toward `target` (ease-out expo,
 * ~900ms). The real value is always `target`; this is presentation only.
 */
export function useCountUp(target: Ref<number>, duration = 900): Ref<number> {
  const shown = ref(reducedMotion() ? target.value : 0)
  let raf = 0
  watch(
    target,
    (to) => {
      cancelAnimationFrame(raf)
      if (reducedMotion() || typeof requestAnimationFrame === 'undefined') {
        shown.value = to
        return
      }
      const from = shown.value
      const start = performance.now()
      const step = (now: number) => {
        const t = Math.min(1, (now - start) / duration)
        const eased = t === 1 ? 1 : 1 - 2 ** (-10 * t)
        shown.value = from + (to - from) * eased
        if (t < 1) raf = requestAnimationFrame(step)
      }
      raf = requestAnimationFrame(step)
    },
    { immediate: true },
  )
  onUnmounted(() => cancelAnimationFrame(raf))
  return shown
}

declare module 'vue' {
  interface GlobalDirectives {
    vReveal: typeof vReveal
  }
}
