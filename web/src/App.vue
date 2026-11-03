<script setup lang="ts">
import AppIcon from './components/AppIcon.vue'
import AppShell from './components/AppShell.vue'
import { useToasts } from './stores/toast'

const toasts = useToasts()
</script>

<template>
  <AppShell>
    <RouterView v-slot="{ Component, route }">
      <Transition name="route" mode="out-in">
        <component :is="Component" :key="route.name === 'digests' ? 'digests' : route.fullPath.split('?')[0]" />
      </Transition>
    </RouterView>
  </AppShell>
  <div
    class="pointer-events-none fixed inset-x-4 bottom-24 z-50 flex flex-col items-center gap-2 md:inset-x-auto md:right-6 md:bottom-6 md:items-end"
    aria-live="polite"
    aria-atomic="false"
  >
    <TransitionGroup name="toast">
      <div
        v-for="t in toasts.items"
        :key="t.id"
        class="pointer-events-auto flex w-full max-w-sm items-start gap-3 rounded-md border border-l-4 bg-surface px-4 py-3 text-sm shadow-2xl shadow-black/60"
        :class="t.kind === 'error' ? 'border-line border-l-danger' : t.kind === 'success' ? 'border-line border-l-primary' : 'border-line border-l-white/40'"
        :role="t.kind === 'error' ? 'alert' : 'status'"
        data-testid="toast"
      >
        <AppIcon :name="t.kind === 'error' ? 'alert' : 'check'" :size="18" :class="t.kind === 'error' ? 'text-danger' : 'text-primary'" />
        <p class="flex-1 break-words">{{ t.text }}</p>
        <button type="button" class="-m-2 inline-flex size-11 cursor-pointer items-center justify-center rounded-md text-muted hover:text-white" aria-label="Fechar aviso" data-testid="toast-close" @click="toasts.dismiss(t.id)">
          <AppIcon name="close" :size="16" />
        </button>
      </div>
    </TransitionGroup>
  </div>
</template>
