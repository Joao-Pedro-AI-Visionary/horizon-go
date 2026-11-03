<script setup lang="ts">
import { ref, watch } from 'vue'

// Native <dialog>: focus trap, Esc to close and inert background for free.
const props = defineProps<{ open: boolean; title: string; message: string; confirmLabel: string; busy?: boolean }>()
const emit = defineEmits<{ confirm: []; cancel: [] }>()
const el = ref<HTMLDialogElement>()

watch(
  () => props.open,
  (open) => {
    if (!el.value) return
    if (open && !el.value.open) el.value.showModal()
    if (!open && el.value.open) el.value.close()
  },
)
</script>

<template>
  <dialog
    ref="el"
    class="card m-auto w-[min(92vw,420px)] p-0 text-white backdrop:bg-black/70"
    aria-labelledby="confirm-title"
    data-testid="confirm-dialog"
    @cancel.prevent="emit('cancel')"
  >
    <div class="p-6">
      <h2 id="confirm-title" class="font-display text-lg font-semibold">{{ title }}</h2>
      <p class="mt-2 text-sm text-muted">{{ message }}</p>
      <div class="mt-6 flex justify-end gap-2">
        <button type="button" class="btn-ghost" data-testid="confirm-cancel" @click="emit('cancel')">Cancelar</button>
        <button type="button" class="btn-danger" :disabled="busy" data-testid="confirm-ok" @click="emit('confirm')">
          {{ busy ? 'Excluindo…' : confirmLabel }}
        </button>
      </div>
    </div>
  </dialog>
</template>
