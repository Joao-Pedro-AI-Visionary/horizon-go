<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ApiError, api, errorMessage } from '../api/client'
import type { CollectReport, Settings, Source } from '../api/types'
import AppIcon from '../components/AppIcon.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import SourceForm from '../components/SourceForm.vue'
import PageHeader from '../components/PageHeader.vue'
import StatePanel from '../components/StatePanel.vue'
import { fmtDateTime, fmtNumber, fmtRelative, kindLabel } from '../lib/format'
import { useToasts } from '../stores/toast'

const toasts = useToasts()

// --- Sources ---
const sources = ref<Source[]>([])
const loading = ref(true)
const error = ref('')
async function load() {
  loading.value = true
  error.value = ''
  try {
    sources.value = (await api.sources()).sources ?? []
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

const formOpen = ref(false)
const editing = ref<Source | null>(null)
function openCreate() {
  editing.value = null
  formOpen.value = true
}
function openEdit(s: Source) {
  editing.value = s
  formOpen.value = true
}
async function onSaved(s: Source) {
  formOpen.value = false
  toasts.success(editing.value ? `Fonte “${s.name}” atualizada.` : `Fonte “${s.name}” criada.`)
  await load()
}

const busy = reactive<Record<number, string>>({})
async function toggle(s: Source) {
  busy[s.id] = 'toggle'
  try {
    const u = await api.updateSource(s.id, { enabled: !s.enabled })
    s.enabled = u.enabled
    toasts.success(`${s.name} ${u.enabled ? 'ativada' : 'desativada'}.`)
  } catch (err) {
    toasts.error(errorMessage(err))
  } finally {
    delete busy[s.id]
  }
}

async function collect(s: Source) {
  busy[s.id] = 'fetch'
  try {
    let rep: CollectReport
    try {
      rep = await api.fetchSource(s.id)
    } catch (err) {
      if (err instanceof ApiError && err.status === 502 && err.body && typeof err.body === 'object' && 'run_id' in err.body) rep = err.body as CollectReport
      else throw err
    }
    const run = rep.sources[0]
    if (run && run.status !== 'ok') toasts.error(`${s.name}: ${run.error || 'falha na coleta'}`)
    else toasts.success(`${s.name}: ${rep.fetched} coletados, ${rep.published} novos enviados ao worker.`)
    await load()
  } catch (err) {
    toasts.error(errorMessage(err))
  } finally {
    delete busy[s.id]
  }
}

const toDelete = ref<Source | null>(null)
const deleting = ref(false)
async function confirmDelete() {
  if (!toDelete.value) return
  deleting.value = true
  try {
    await api.deleteSource(toDelete.value.id)
    toasts.success(`Fonte “${toDelete.value.name}” excluída.`)
    toDelete.value = null
    await load()
  } catch (err) {
    toasts.error(errorMessage(err))
  } finally {
    deleting.value = false
  }
}

function configSummary(s: Source): string {
  const c = s.config as Record<string, unknown>
  if (s.kind === 'rss') return String(c.url ?? '')
  const parts: string[] = []
  if (s.kind === 'reddit') parts.push(`r/${c.subreddit}`, String(c.sort ?? 'hot'))
  if (c.limit) parts.push(`limite ${c.limit}`)
  if (c.min_score !== undefined) parts.push(`≥ ${c.min_score} pts`)
  return parts.join(' · ') || 'configuração padrão'
}

const enabledCount = computed(() => sources.value.filter((s) => s.enabled).length)

// --- Settings ---
const settings = ref<Settings | null>(null)
const draft = reactive<Settings>({ score_threshold: 6, digest_max_items: 20, digest_per_source: 0, digest_cron: '', timezone: '' })
const settingsError = ref('')
const settingsLoading = ref(true)
const saving = ref(false)
const saveError = ref('')
async function loadSettings() {
  settingsLoading.value = true
  settingsError.value = ''
  try {
    settings.value = await api.settings()
    Object.assign(draft, settings.value)
  } catch (err) {
    settingsError.value = errorMessage(err)
  } finally {
    settingsLoading.value = false
  }
}
async function saveSettings() {
  saving.value = true
  saveError.value = ''
  try {
    settings.value = await api.saveSettings({
      score_threshold: Number(draft.score_threshold),
      digest_max_items: Number(draft.digest_max_items),
      digest_per_source: Number(draft.digest_per_source),
      digest_cron: draft.digest_cron.trim(),
      timezone: draft.timezone.trim(),
    })
    Object.assign(draft, settings.value)
    toasts.success('Configurações salvas. O worker aplica o novo cron no próximo ciclo.')
  } catch (err) {
    saveError.value = errorMessage(err)
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  load()
  loadSettings()
})
</script>

<template>
  <div>
    <PageHeader index="03" section="Fontes & Config" title="Fontes e configuração" :muted="2" lead="Onde o radar escuta e como ele decide o que entra no digest.">
      <template #meta><span class="num">{{ sources.length }}</span> fontes · <span class="num">{{ enabledCount }}</span> ativas</template>
      <template #actions>
        <button type="button" class="btn-primary" data-testid="source-new" @click="openCreate">
          <AppIcon name="plus" :size="16" /> Nova fonte
        </button>
      </template>
    </PageHeader>

    <div class="mt-8 grid gap-10 xl:grid-cols-12">
      <!-- Sources -->
      <section class="min-w-0 xl:col-span-8" aria-labelledby="sources-h">
        <h2 id="sources-h" class="eyebrow mb-3">Fontes cadastradas</h2>
        <div v-if="loading && sources.length === 0" class="grid gap-3" data-testid="sources-loading" aria-hidden="true">
          <div v-for="i in 3" :key="i" class="skeleton h-36" />
        </div>
        <StatePanel v-else-if="error" kind="error" title="Não foi possível carregar as fontes" :detail="error" @retry="load" />
        <StatePanel v-else-if="sources.length === 0" kind="empty" title="Nenhuma fonte cadastrada" detail="Cadastre um feed RSS, um subreddit ou o Hacker News para começar a coletar." testid="sources-empty">
          <button type="button" class="btn-primary" data-testid="empty-source-new" @click="openCreate"><AppIcon name="plus" :size="16" /> Nova fonte</button>
        </StatePanel>
        <ul v-else class="border-t border-line" data-testid="source-list">
          <li v-for="(s, i) in sources" :key="s.id" v-reveal="i" class="border-b border-line py-6" :class="s.enabled ? '' : 'opacity-70'" data-testid="source-card">
            <div class="flex items-start gap-4">
              <span class="num hidden pt-1.5 text-sm text-muted sm:block" aria-hidden="true">{{ String(i + 1).padStart(2, '0') }}</span>
              <div class="min-w-0 flex-1">
                <div class="flex flex-wrap items-center gap-2">
                  <h3 class="font-display text-xl font-semibold tracking-tight break-words md:text-2xl">{{ s.name }}</h3>
                  <span class="chip">{{ kindLabel[s.kind] }}</span>
                  <span v-if="!s.enabled" class="chip border-accent/50 text-accent">desativada</span>
                </div>
                <p class="mt-1 font-mono text-xs break-all text-muted">{{ configSummary(s) }}</p>
              </div>
              <label class="flex min-h-11 shrink-0 cursor-pointer items-center gap-2 text-sm" :title="s.enabled ? 'Desativar' : 'Ativar'">
                <span class="sr-only">{{ s.enabled ? 'Desativar' : 'Ativar' }} {{ s.name }}</span>
                <input
                  type="checkbox"
                  role="switch"
                  class="peer sr-only"
                  :checked="s.enabled"
                  :disabled="!!busy[s.id]"
                  :aria-checked="s.enabled"
                  data-testid="source-toggle"
                  @change="toggle(s)"
                />
                <span
                  class="relative h-7 w-12 rounded-full border transition-colors duration-200 peer-focus-visible:outline-2 peer-focus-visible:outline-offset-2 peer-focus-visible:outline-primary"
                  :class="s.enabled ? 'border-primary bg-primary' : 'border-line bg-raised'"
                  aria-hidden="true"
                >
                  <span class="absolute top-0.5 size-5.5 rounded-full transition-all duration-300 ease-[var(--ease-out-expo)]" :class="s.enabled ? 'left-6 bg-ink' : 'left-0.5 bg-muted'" />
                </span>
              </label>
            </div>

            <dl class="mt-4 grid grid-cols-3 sm:ml-10">
              <div class="pr-3">
                <dt class="eyebrow">Itens</dt>
                <dd class="num mt-1 text-2xl font-bold">{{ fmtNumber(s.item_count ?? 0) }}</dd>
              </div>
              <div class="border-l border-line px-3">
                <dt class="eyebrow">Última coleta</dt>
                <dd class="mt-1.5 text-sm" :title="fmtDateTime(s.last_fetch?.started_at)">{{ s.last_fetch ? fmtRelative(s.last_fetch.started_at) : 'nunca' }}</dd>
              </div>
              <div class="border-l border-line pl-3">
                <dt class="eyebrow">Status</dt>
                <dd class="mt-1.5 flex items-center gap-1.5 text-sm" :class="s.last_fetch?.status === 'error' ? 'text-danger' : ''">
                  <AppIcon v-if="s.last_fetch" :name="s.last_fetch.status === 'ok' ? 'check' : 'alert'" :size="14" :class="s.last_fetch.status === 'ok' ? 'text-ok' : ''" />
                  {{ !s.last_fetch ? '—' : s.last_fetch.status === 'ok' ? `ok · ${s.last_fetch.item_count}` : 'erro' }}
                </dd>
              </div>
            </dl>
            <p v-if="s.last_fetch?.error" class="mt-3 rounded-md bg-danger/10 p-3 text-xs break-words text-danger sm:ml-10">{{ s.last_fetch.error }}</p>

            <div class="mt-4 flex flex-wrap gap-2 sm:ml-10">
              <button type="button" class="btn-primary" :disabled="!!busy[s.id] || !s.enabled" :title="s.enabled ? undefined : 'Ative a fonte para coletar'" data-testid="source-fetch" @click="collect(s)">
                <AppIcon name="refresh" :size="16" :class="busy[s.id] === 'fetch' ? 'animate-spin' : ''" />
                {{ busy[s.id] === 'fetch' ? 'Coletando…' : 'Coletar agora' }}
              </button>
              <button type="button" class="btn-ghost" :disabled="!!busy[s.id]" data-testid="source-edit" @click="openEdit(s)"><AppIcon name="edit" :size="16" /> Editar</button>
              <button type="button" class="btn-danger" :disabled="!!busy[s.id]" data-testid="source-delete" @click="toDelete = s"><AppIcon name="trash" :size="16" /> Excluir</button>
            </div>
          </li>
        </ul>
      </section>

      <!-- Settings -->
      <section class="card h-fit p-5 md:p-6 xl:sticky xl:top-24 xl:col-span-4" aria-labelledby="settings-h" data-testid="settings">
        <p class="eyebrow text-primary">Configuração</p>
        <h2 id="settings-h" class="mt-1 font-display text-2xl font-bold tracking-tight">Digest e nota de corte</h2>
        <div v-if="settingsLoading && !settings" class="mt-5 space-y-3" data-testid="settings-loading" aria-hidden="true">
          <div v-for="i in 4" :key="i" class="skeleton h-11" />
        </div>
        <StatePanel v-else-if="settingsError" class="mt-4" kind="error" title="Configurações indisponíveis" :detail="settingsError" @retry="loadSettings" />
        <form v-else class="mt-5 grid gap-5" novalidate @submit.prevent="saveSettings">
          <div>
            <div class="flex items-end justify-between">
              <label for="st-th" class="label">Nota de corte</label>
              <span class="num text-3xl leading-none font-bold text-primary" aria-hidden="true">{{ Number(draft.score_threshold).toFixed(1) }}</span>
            </div>
            <input id="st-th" v-model.number="draft.score_threshold" type="range" min="0" max="10" step="0.5" class="h-11 w-full cursor-pointer accent-primary" :aria-valuetext="`${draft.score_threshold} de 10`" aria-describedby="st-th-h" data-testid="settings-threshold" />
            <p id="st-th-h" class="text-xs text-muted">Só itens com nota ≥ corte entram no digest.</p>
          </div>
          <div class="grid grid-cols-2 gap-3">
            <div>
              <label for="st-max" class="label">Máx. itens</label>
              <input id="st-max" v-model.number="draft.digest_max_items" type="number" min="1" max="200" class="field num" data-testid="settings-max" />
            </div>
            <div>
              <label for="st-per" class="label">Por fonte</label>
              <input id="st-per" v-model.number="draft.digest_per_source" type="number" min="0" max="200" class="field num" aria-describedby="st-per-h" data-testid="settings-per-source" />
            </div>
          </div>
          <p id="st-per-h" class="-mt-3 text-xs text-muted">Por fonte = 0 desliga a cota.</p>
          <div>
            <label for="st-cron" class="label">Cron do digest</label>
            <input id="st-cron" v-model="draft.digest_cron" class="field font-mono" placeholder="0 7 * * *" aria-describedby="st-cron-h" data-testid="settings-cron" />
            <p id="st-cron-h" class="mt-1 text-xs text-muted">minuto hora dia mês dia-da-semana (ex.: 0 7 * * * = 07:00).</p>
          </div>
          <div>
            <label for="st-tz" class="label">Fuso horário</label>
            <input id="st-tz" v-model="draft.timezone" class="field" placeholder="America/Sao_Paulo" data-testid="settings-timezone" />
          </div>
          <p v-if="saveError" class="rounded-md bg-danger/10 p-3 text-sm break-words text-danger" role="alert" data-testid="settings-error">{{ saveError }}</p>
          <button type="submit" class="btn-primary w-full" :disabled="saving" data-testid="settings-save">{{ saving ? 'Salvando…' : 'Salvar configurações' }}</button>
          <p v-if="settings?.updated_at" class="num text-center text-xs text-muted">atualizado {{ fmtDateTime(settings.updated_at) }}</p>
        </form>
      </section>
    </div>

    <SourceForm :open="formOpen" :source="editing" @close="formOpen = false" @saved="onSaved" />
    <ConfirmDialog
      :open="toDelete !== null"
      title="Excluir fonte?"
      :message="toDelete ? `“${toDelete.name}” e os itens coletados dela serão removidos. Essa ação não pode ser desfeita.` : ''"
      confirm-label="Excluir"
      :busy="deleting"
      @cancel="toDelete = null"
      @confirm="confirmDelete"
    />
  </div>
</template>
