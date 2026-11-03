<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { api, errorMessage } from '../api/client'
import type { Source, SourceKind } from '../api/types'
import { kindLabel } from '../lib/format'
import AppIcon from './AppIcon.vue'

// Create/edit a source in a native modal <dialog>. Kind-specific config keys
// mirror the Go adapters (source/hn, source/reddit, source/rss).
const props = defineProps<{ open: boolean; source: Source | null }>()
const emit = defineEmits<{ close: []; saved: [Source] }>()

const el = ref<HTMLDialogElement>()
const form = reactive({
  kind: 'rss' as SourceKind,
  name: '',
  enabled: true,
  url: '',
  subreddit: '',
  sort: 'hot',
  time_filter: '',
  limit: '' as number | '',
  min_score: '' as number | '',
})
const saving = ref(false)
const error = ref('')

watch(
  () => props.open,
  (open) => {
    if (!el.value) return
    if (open) {
      const s = props.source
      const c = (s?.config ?? {}) as Record<string, unknown>
      Object.assign(form, {
        kind: s?.kind ?? 'rss',
        name: s?.name ?? '',
        enabled: s?.enabled ?? true,
        url: (c.url as string) ?? '',
        subreddit: (c.subreddit as string) ?? '',
        sort: (c.sort as string) ?? 'hot',
        time_filter: (c.time_filter as string) ?? '',
        limit: (c.limit as number) ?? '',
        min_score: (c.min_score as number) ?? '',
      })
      error.value = ''
      if (!el.value.open) el.value.showModal()
    } else if (el.value.open) {
      el.value.close()
    }
  },
)

const editing = computed(() => props.source !== null)

function config(): Record<string, unknown> {
  const num = (v: number | '') => (v === '' || v === null ? undefined : Number(v))
  const out: Record<string, unknown> = {}
  if (form.kind === 'rss') out.url = form.url.trim()
  if (form.kind === 'reddit') {
    out.subreddit = form.subreddit.trim().replace(/^r\//, '')
    out.sort = form.sort
    if (form.sort === 'top' && form.time_filter) out.time_filter = form.time_filter
  }
  if (form.kind !== 'rss') {
    if (num(form.limit) !== undefined) out.limit = num(form.limit)
    if (num(form.min_score) !== undefined) out.min_score = num(form.min_score)
  }
  return out
}

async function submit() {
  saving.value = true
  error.value = ''
  try {
    const body = { name: form.name.trim(), config: config(), enabled: form.enabled }
    const saved = props.source ? await api.updateSource(props.source.id, body) : await api.createSource({ ...body, kind: form.kind })
    emit('saved', saved)
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <dialog
    ref="el"
    class="card m-auto max-h-[90dvh] w-[min(94vw,520px)] overflow-y-auto p-0 text-white backdrop:bg-black/70"
    aria-labelledby="source-form-title"
    data-testid="source-form"
    @cancel.prevent="emit('close')"
  >
    <form class="p-6" novalidate @submit.prevent="submit">
      <div class="flex items-center justify-between gap-3">
        <h2 id="source-form-title" class="font-display text-xl font-semibold">{{ editing ? 'Editar fonte' : 'Nova fonte' }}</h2>
        <button type="button" class="btn-icon" aria-label="Fechar formulário" data-testid="source-form-close" @click="emit('close')">
          <AppIcon name="close" :size="16" />
        </button>
      </div>

      <div class="mt-5 grid gap-4">
        <fieldset>
          <legend class="label">Tipo</legend>
          <div class="grid grid-cols-3 gap-2">
            <label
              v-for="k in (['hackernews', 'reddit', 'rss'] as const)"
              :key="k"
              class="flex min-h-11 items-center justify-center rounded-md border px-2 text-sm font-medium has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-primary"
              :class="[form.kind === k ? 'border-primary bg-primary/10 text-primary' : 'border-line bg-raised', editing ? 'cursor-not-allowed opacity-60' : 'cursor-pointer']"
            >
              <input v-model="form.kind" type="radio" name="kind" :value="k" class="sr-only" :disabled="editing" :data-testid="`source-kind-${k}`" />
              {{ kindLabel[k] }}
            </label>
          </div>
          <p v-if="editing" class="mt-1 text-xs text-muted">O tipo não muda depois de criado.</p>
        </fieldset>

        <div>
          <label for="sf-name" class="label">Nome</label>
          <input id="sf-name" v-model="form.name" class="field" required maxlength="100" placeholder="ex.: The Go Blog" data-testid="source-name" />
        </div>

        <div v-if="form.kind === 'rss'">
          <label for="sf-url" class="label">URL do feed (RSS/Atom)</label>
          <input id="sf-url" v-model="form.url" type="url" inputmode="url" class="field" required placeholder="https://go.dev/blog/feed.atom" data-testid="source-url" />
        </div>

        <template v-if="form.kind === 'reddit'">
          <div>
            <label for="sf-sub" class="label">Subreddit</label>
            <input id="sf-sub" v-model="form.subreddit" class="field" required placeholder="golang" pattern="[A-Za-z0-9_]{2,21}" data-testid="source-subreddit" />
          </div>
          <div class="grid grid-cols-2 gap-3">
            <div>
              <label for="sf-sort" class="label">Ordenação</label>
              <select id="sf-sort" v-model="form.sort" class="field" data-testid="source-sort">
                <option value="hot">hot</option>
                <option value="new">new</option>
                <option value="top">top</option>
                <option value="rising">rising</option>
              </select>
            </div>
            <div>
              <label for="sf-tf" class="label">Janela (top)</label>
              <select id="sf-tf" v-model="form.time_filter" class="field" :disabled="form.sort !== 'top'" data-testid="source-time-filter">
                <option value="">padrão</option>
                <option v-for="t in ['hour', 'day', 'week', 'month', 'year', 'all']" :key="t" :value="t">{{ t }}</option>
              </select>
            </div>
          </div>
        </template>

        <div v-if="form.kind !== 'rss'" class="grid grid-cols-2 gap-3">
          <div>
            <label for="sf-limit" class="label">Limite de itens</label>
            <input id="sf-limit" v-model.number="form.limit" type="number" min="1" max="100" class="field" placeholder="padrão" data-testid="source-limit" />
          </div>
          <div>
            <label for="sf-min" class="label">Pontos mínimos</label>
            <input id="sf-min" v-model.number="form.min_score" type="number" min="0" class="field" placeholder="padrão" data-testid="source-min-score" />
          </div>
        </div>

        <label class="flex min-h-11 cursor-pointer items-center gap-3">
          <input v-model="form.enabled" type="checkbox" class="size-5 accent-primary" data-testid="source-enabled" />
          <span class="text-sm">Ativa (entra nas coletas)</span>
        </label>

        <p v-if="error" class="rounded-lg bg-danger/10 p-3 text-sm break-words text-danger" role="alert" data-testid="source-form-error">{{ error }}</p>
      </div>

      <div class="mt-6 flex justify-end gap-2">
        <button type="button" class="btn-ghost" data-testid="source-cancel" @click="emit('close')">Cancelar</button>
        <button type="submit" class="btn-primary" :disabled="saving" data-testid="source-save">{{ saving ? 'Salvando…' : editing ? 'Salvar' : 'Criar fonte' }}</button>
      </div>
    </form>
  </dialog>
</template>
