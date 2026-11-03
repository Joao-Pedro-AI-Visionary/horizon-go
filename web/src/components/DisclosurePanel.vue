<script setup lang="ts">
import { useId } from 'vue'
import AppIcon from './AppIcon.vue'

// Disclosure (WAI-ARIA pattern): a button with aria-expanded/aria-controls
// opens a region whose height animates (grid rows 0fr -> 1fr,
// var(--dur-disclose) ease-out) while its children fade in and rise 10px,
// staggered. Closed content is visibility:hidden, so it leaves the tab order
// and the accessibility tree. Styles live in style.css (.disclosure*).
const open = defineModel<boolean>('open', { default: false })
defineProps<{ title: string; testid: string; tone?: 'plain' | 'float' }>()
const id = useId()
</script>

<template>
  <div class="disclosure" :class="[open ? 'is-open' : '', tone === 'float' ? 'card-float' : '']" :data-testid="testid" :data-open="open">
    <button
      :id="`${id}-btn`"
      type="button"
      class="flex min-h-11 w-full cursor-pointer items-center gap-3 rounded-[var(--radius-control)] text-left"
      :class="tone === 'float' ? 'px-4 md:px-5' : ''"
      :aria-expanded="open"
      :aria-controls="`${id}-body`"
      :data-testid="`${testid}-toggle`"
      @click="open = !open"
    >
      <span class="min-w-0 flex-1"><slot name="summary">{{ title }}</slot></span>
      <AppIcon name="chevronDown" :size="18" class="disclosure-chevron shrink-0 text-muted" />
    </button>
    <div :id="`${id}-body`" class="disclosure-body" role="region" :aria-labelledby="`${id}-btn`" :data-testid="`${testid}-body`">
      <div class="disclosure-inner">
        <div :class="tone === 'float' ? 'px-4 pb-5 md:px-5' : 'pt-3'"><slot /></div>
      </div>
    </div>
  </div>
</template>
