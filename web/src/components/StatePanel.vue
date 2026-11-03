<script setup lang="ts">
import AppIcon from './AppIcon.vue'

// Empty / error state with an optional retry action.
defineProps<{ kind: 'empty' | 'error'; title: string; detail?: string; testid?: string }>()
defineEmits<{ retry: [] }>()
</script>

<template>
  <div
    class="card flex flex-col items-center gap-3 px-6 py-10 text-center"
    :role="kind === 'error' ? 'alert' : 'status'"
    :data-testid="testid ?? `state-${kind}`"
  >
    <span
      class="inline-flex size-12 items-center justify-center rounded-full"
      :class="kind === 'error' ? 'bg-danger/15 text-danger' : 'bg-primary/10 text-primary'"
    >
      <AppIcon :name="kind === 'error' ? 'alert' : 'layers'" :size="22" />
    </span>
    <p class="font-display text-lg font-semibold">{{ title }}</p>
    <p v-if="detail" class="max-w-md text-sm text-muted">{{ detail }}</p>
    <div v-if="$slots.default || kind === 'error'" class="mt-1 flex flex-wrap justify-center gap-2">
      <slot />
      <button v-if="kind === 'error'" type="button" class="btn-ghost" data-testid="retry" @click="$emit('retry')">
        <AppIcon name="refresh" :size="16" /> Tentar de novo
      </button>
    </div>
  </div>
</template>
