import { defineStore } from 'pinia'
import { ref } from 'vue'

export interface Toast {
  id: number
  kind: 'success' | 'error' | 'info'
  text: string
}

/** Toasts announce the result of an action (aria-live region in App). */
export const useToasts = defineStore('toasts', () => {
  const items = ref<Toast[]>([])
  let seq = 0

  function dismiss(id: number) {
    items.value = items.value.filter((t) => t.id !== id)
  }

  function push(kind: Toast['kind'], text: string, ms = kind === 'error' ? 8000 : 5000) {
    const id = ++seq
    items.value.push({ id, kind, text })
    setTimeout(() => dismiss(id), ms)
  }

  return {
    items,
    dismiss,
    success: (t: string) => push('success', t),
    error: (t: string) => push('error', t),
    info: (t: string) => push('info', t),
  }
})
