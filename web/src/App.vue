<script setup lang="ts">
import AppIcon from './components/AppIcon.vue'
import AppShell from './components/AppShell.vue'
import { useToasts } from './stores/toast'

const toasts = useToasts()
</script>

<template>
  <AppShell>
    <RouterView />
  </AppShell>
  <div
    class="pointer-events-none fixed inset-x-4 bottom-24 z-50 flex flex-col items-center gap-2 md:inset-x-auto md:right-6 md:bottom-6 md:items-end"
    aria-live="polite"
    aria-atomic="false"
  >
    <div
      v-for="t in toasts.items"
      :key="t.id"
      class="pointer-events-auto flex w-full max-w-sm items-start gap-3 rounded-xl border bg-raised px-4 py-3 text-sm shadow-2xl"
      :class="t.kind === 'error' ? 'border-danger/50' : t.kind === 'success' ? 'border-primary/50' : 'border-line'"
      :role="t.kind === 'error' ? 'alert' : 'status'"
      data-testid="toast"
    >
      <AppIcon :name="t.kind === 'error' ? 'alert' : 'check'" :size="18" :class="t.kind === 'error' ? 'text-danger' : 'text-primary'" />
      <p class="flex-1 break-words">{{ t.text }}</p>
      <button type="button" class="-m-2 inline-flex size-9 items-center justify-center rounded-lg text-muted hover:text-white" aria-label="Fechar aviso" data-testid="toast-close" @click="toasts.dismiss(t.id)">
        <AppIcon name="close" :size="16" />
      </button>
    </div>
  </div>
</template>
