<script setup lang="ts">
import AppIcon from './AppIcon.vue'

// Empty / error state with an optional retry action.
defineProps<{ kind: 'empty' | 'error'; title: string; detail?: string; testid?: string }>()
defineEmits<{ retry: [] }>()
</script>

<template>
  <div
    class="flex flex-col items-start gap-3 border-y border-line py-12 md:py-16"
    :role="kind === 'error' ? 'alert' : 'status'"
    :data-testid="testid ?? `state-${kind}`"
  >
    <span class="eyebrow flex items-center gap-2" :class="kind === 'error' ? 'text-danger' : 'text-primary'">
      <AppIcon :name="kind === 'error' ? 'alert' : 'layers'" :size="14" /> {{ kind === 'error' ? 'Erro' : 'Vazio' }}
    </span>
    <p class="display-lg max-w-3xl">{{ title }}</p>
    <p v-if="detail" class="max-w-xl text-white/70">{{ detail }}</p>
    <div v-if="$slots.default || kind === 'error'" class="mt-3 flex flex-wrap gap-2">
      <slot />
      <button v-if="kind === 'error'" type="button" class="btn-ghost" data-testid="retry" @click="$emit('retry')">
        <AppIcon name="refresh" :size="16" /> Tentar de novo
      </button>
    </div>
  </div>
</template>
