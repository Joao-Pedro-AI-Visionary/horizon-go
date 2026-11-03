<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { ApiError, api, errorMessage } from '../api/client'
import type { CollectReport, FeedItem, FeedPage, FeedParams, Source } from '../api/types'
import AppIcon from '../components/AppIcon.vue'
import ScoreBadge from '../components/ScoreBadge.vue'
import StatePanel from '../components/StatePanel.vue'
import { fmtNumber, fmtRelative, hostOf } from '../lib/format'
import { useToasts } from '../stores/toast'

const route = useRoute()
const router = useRouter()
const toasts = useToasts()
const PAGE_SIZE = 12

// Filters live in the URL so every view of the feed is a deep link.
const filters = computed<FeedParams>(() => {
  const q = route.query
  const num = (v: unknown) => (v === undefined || v === '' ? undefined : Number(v))
  return {
    q: (q.q as string) || undefined,
    tag: (q.tag as string) || undefined,
    source_id: num(q.source_id),
    period: ((q.period as string) || '7d') as FeedParams['period'],
    min_score: num(q.min_score),
    sort: ((q.sort as string) || 'score') as FeedParams['sort'],
    page: num(q.page) ?? 1,
    page_size: PAGE_SIZE,
  }
})

function setFilter(patch: Record<string, string | number | undefined>) {
  const next: Record<string, string> = {}
  const merged = { ...route.query, page: undefined, ...patch }
  for (const [k, v] of Object.entries(merged)) if (v !== undefined && v !== '' && v !== null) next[k] = String(v)
  router.replace({ query: next })
}

// On mobile the filters below the search are collapsed behind a toggle.
const filtersOpen = ref(false)
const activeFilterCount = computed(() => {
  const f = filters.value
  return [f.tag, f.source_id, f.min_score, f.period !== '7d' ? f.period : undefined, f.sort !== 'score' ? f.sort : undefined].filter(Boolean).length
})

const searchText = ref(filters.value.q ?? '')
let debounce: ReturnType<typeof setTimeout> | undefined
watch(searchText, (v) => {
  clearTimeout(debounce)
  debounce = setTimeout(() => setFilter({ q: v.trim() || undefined }), 350)
})
const minScore = ref(filters.value.min_score ?? 0)
const tagText = ref(filters.value.tag ?? '')
watch(
  () => route.query,
  () => {
    tagText.value = filters.value.tag ?? ''
    minScore.value = filters.value.min_score ?? 0
    if ((filters.value.q ?? '') !== searchText.value.trim()) searchText.value = filters.value.q ?? ''
  },
)

const page = ref<FeedPage | null>(null)
const loading = ref(true)
const error = ref('')
let ctrl: AbortController | undefined

async function load() {
  ctrl?.abort()
  ctrl = new AbortController()
  loading.value = true
  error.value = ''
  try {
    page.value = await api.feed(filters.value, ctrl.signal)
  } catch (err) {
    if ((err as Error).name === 'AbortError') return
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}
watch(filters, load, { deep: true })

const sources = ref<Source[]>([])
const threshold = ref(6)
onMounted(async () => {
  load()
  const [s, st] = await Promise.allSettled([api.sources(), api.settings()])
  if (s.status === 'fulfilled') sources.value = s.value.sources ?? []
  if (st.status === 'fulfilled') threshold.value = st.value.score_threshold
})
onUnmounted(() => ctrl?.abort())

const items = computed<FeedItem[]>(() => page.value?.items ?? [])
const totalPages = computed(() => (page.value ? Math.max(1, Math.ceil(page.value.total / page.value.page_size)) : 1))
const hasFilters = computed(() => {
  const f = filters.value
  return !!(f.q || f.tag || f.source_id || f.min_score || f.period !== '7d')
})

// Pipeline run (POST /api/pipeline/run): collects every enabled source and
// publishes to NATS; the worker scores and dedups before items reach the feed.
const running = ref(false)
const report = ref<CollectReport | null>(null)
async function runPipeline() {
  running.value = true
  try {
    report.value = await api.runPipeline()
    toasts.success(`Coleta concluída: ${report.value.published} novos itens publicados para o worker.`)
  } catch (err) {
    if (err instanceof ApiError && err.status === 502 && err.body && typeof err.body === 'object' && 'run_id' in err.body) {
      report.value = err.body as CollectReport
      toasts.error(`Coleta terminou com erros: ${report.value.error ?? 'veja o relatório'}`)
    } else {
      toasts.error(errorMessage(err))
    }
  } finally {
    running.value = false
  }
  load()
}
</script>

<template>
  <div class="mx-auto max-w-7xl">
    <header class="flex flex-wrap items-end justify-between gap-4">
      <div>
        <p class="eyebrow">Radar · feed</p>
        <h1 class="mt-1 font-display text-3xl font-bold md:text-4xl">O que importa hoje</h1>
        <p class="mt-1 text-sm text-muted">
          Itens ranqueados pela nota do LLM, um por cluster de duplicados.
          <span v-if="page" class="font-mono text-white" data-testid="feed-total">{{ fmtNumber(page.total) }} itens</span>
        </p>
      </div>
      <button type="button" class="btn-primary" :disabled="running" :aria-busy="running" data-testid="run-pipeline" @click="runPipeline">
        <AppIcon :name="running ? 'refresh' : 'play'" :size="16" :class="running ? 'animate-spin' : ''" />
        {{ running ? 'Coletando fontes…' : 'Rodar pipeline' }}
      </button>
    </header>

    <!-- Last pipeline report -->
    <section v-if="report" class="card mt-5 p-4" aria-labelledby="report-title" data-testid="pipeline-report">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <h2 id="report-title" class="font-display font-semibold">Última coleta <span class="font-mono text-xs text-muted">{{ report.run_id }}</span></h2>
        <button type="button" class="btn-icon size-9" aria-label="Fechar relatório" data-testid="close-report" @click="report = null">
          <AppIcon name="close" :size="16" />
        </button>
      </div>
      <dl class="mt-3 grid grid-cols-2 gap-3 sm:grid-cols-4">
        <div v-for="k in (['fetched', 'published', 'duplicates', 'invalid'] as const)" :key="k" class="rounded-xl bg-raised p-3">
          <dt class="text-xs text-muted">{{ { fetched: 'Coletados', published: 'Publicados', duplicates: 'Já vistos', invalid: 'Inválidos' }[k] }}</dt>
          <dd class="font-mono text-2xl font-bold">{{ report[k] }}</dd>
        </div>
      </dl>
      <ul class="mt-3 flex flex-wrap gap-2">
        <li v-for="s in report.sources" :key="s.source_id" class="chip" :class="s.status === 'ok' ? '' : 'border-danger/50 text-danger'" :title="s.error || undefined">
          {{ s.source_name }} · {{ s.status === 'ok' ? `${s.item_count} itens` : 'erro' }}
        </li>
      </ul>
      <p class="mt-3 text-xs text-muted">Os itens novos entram no feed depois que o worker dá a nota e faz o dedup.</p>
    </section>

    <!-- Filters -->
    <section class="card mt-5 p-4" aria-label="Filtros do feed">
      <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-12">
        <div class="sm:col-span-2 lg:col-span-4">
          <label for="f-search" class="label">Busca</label>
          <div class="relative">
            <AppIcon name="search" :size="16" class="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-muted" />
            <input id="f-search" v-model="searchText" type="search" class="field pl-9" placeholder="Título, resumo ou motivo…" maxlength="200" data-testid="filter-search" />
          </div>
        </div>
        <button
          type="button"
          class="btn-ghost w-full sm:hidden"
          :aria-expanded="filtersOpen"
          aria-controls="feed-more-filters"
          data-testid="toggle-filters"
          @click="filtersOpen = !filtersOpen"
        >
          <AppIcon name="filter" :size="16" />
          {{ filtersOpen ? 'Ocultar filtros' : 'Filtros' }}<span v-if="activeFilterCount" class="rounded-full bg-primary px-2 font-mono text-xs text-ink">{{ activeFilterCount }}</span>
        </button>
        <div id="feed-more-filters" class="contents" :class="filtersOpen ? '' : 'max-sm:hidden'">
        <div class="lg:col-span-2">
          <label for="f-source" class="label">Fonte</label>
          <select id="f-source" class="field" :value="filters.source_id ?? ''" data-testid="filter-source" @change="setFilter({ source_id: ($event.target as HTMLSelectElement).value || undefined })">
            <option value="">Todas</option>
            <option v-for="s in sources" :key="s.id" :value="s.id">{{ s.name }}</option>
          </select>
        </div>
        <div class="lg:col-span-2">
          <label for="f-tag" class="label">Tag</label>
          <input
            id="f-tag"
            v-model="tagText"
            class="field"
            placeholder="ex.: ai"
            maxlength="200"
            data-testid="filter-tag"
            @change="setFilter({ tag: tagText.trim() || undefined })"
            @keydown.enter="setFilter({ tag: tagText.trim() || undefined })"
          />
        </div>
        <div class="lg:col-span-2">
          <label for="f-period" class="label">Período</label>
          <select id="f-period" class="field" :value="filters.period" data-testid="filter-period" @change="setFilter({ period: ($event.target as HTMLSelectElement).value })">
            <option value="24h">Últimas 24h</option>
            <option value="7d">7 dias</option>
            <option value="30d">30 dias</option>
            <option value="all">Tudo</option>
          </select>
        </div>
        <div class="lg:col-span-2">
          <label for="f-sort" class="label">Ordem</label>
          <select id="f-sort" class="field" :value="filters.sort" data-testid="filter-sort" @change="setFilter({ sort: ($event.target as HTMLSelectElement).value })">
            <option value="score">Maior nota</option>
            <option value="recent">Mais recentes</option>
          </select>
        </div>
        <div class="sm:col-span-2 lg:col-span-12">
          <div class="flex items-center justify-between">
            <label for="f-min" class="label">Nota mínima</label>
            <span class="font-mono text-sm text-primary" aria-hidden="true">{{ minScore.toFixed(1) }}</span>
          </div>
          <div class="flex items-center gap-3">
            <input
              id="f-min"
              v-model.number="minScore"
              type="range"
              min="0"
              max="10"
              step="0.5"
              class="h-11 w-full cursor-pointer accent-primary"
              :aria-valuetext="`${minScore} de 10`"
              data-testid="filter-min-score"
              @change="setFilter({ min_score: minScore > 0 ? minScore : undefined })"
            />
            <button v-if="hasFilters" type="button" class="btn-ghost shrink-0" data-testid="clear-filters" @click="router.replace({ query: {} })">Limpar</button>
          </div>
        </div>
        </div>
      </div>
    </section>

    <!-- Results -->
    <section class="mt-5" aria-label="Itens do feed" :aria-busy="loading">
      <div v-if="loading && !page" class="grid gap-4 lg:grid-cols-2" data-testid="feed-loading">
        <div v-for="i in 6" :key="i" class="card flex gap-4 p-5">
          <div class="skeleton size-14 shrink-0" />
          <div class="flex-1 space-y-3">
            <div class="skeleton h-5 w-3/4" />
            <div class="skeleton h-4 w-full" />
            <div class="skeleton h-4 w-2/3" />
          </div>
        </div>
      </div>
      <StatePanel v-else-if="error" kind="error" title="Não foi possível carregar o feed" :detail="error" @retry="load" />
      <StatePanel
        v-else-if="items.length === 0"
        kind="empty"
        :title="hasFilters ? 'Nenhum item com esses filtros' : 'O feed ainda está vazio'"
        :detail="hasFilters ? 'Afrouxe a nota mínima, troque o período ou limpe os filtros.' : 'Rode o pipeline para coletar as fontes ativas; o worker dá a nota e publica no feed.'"
        testid="feed-empty"
      >
        <button v-if="hasFilters" type="button" class="btn-ghost" data-testid="empty-clear" @click="router.replace({ query: {} })">Limpar filtros</button>
      </StatePanel>

      <template v-else>
        <ul class="grid gap-4 transition-opacity duration-200 lg:grid-cols-2" :class="loading ? 'opacity-60' : ''" data-testid="feed-list">
          <li v-for="it in items" :key="it.item_id" class="card group flex gap-4 p-4 transition-colors duration-200 hover:border-white/25 sm:p-5" data-testid="feed-card">
            <ScoreBadge :score="it.score" :threshold="threshold" />
            <div class="min-w-0 flex-1">
              <p class="flex flex-wrap items-center gap-x-2 text-xs text-muted">
                <span class="font-medium text-white/85">{{ it.source_name }}</span>
                <span aria-hidden="true">·</span>
                <span>{{ hostOf(it.url) }}</span>
                <span aria-hidden="true">·</span>
                <time :datetime="it.published_at ?? it.projected_at">{{ fmtRelative(it.published_at ?? it.projected_at) }}</time>
              </p>
              <h2 class="mt-1 font-display text-lg leading-snug font-semibold break-words">
                <RouterLink :to="`/items/${it.item_id}`" class="hover:text-primary" data-testid="open-item">{{ it.title }}</RouterLink>
              </h2>
              <p v-if="it.summary" class="mt-1 line-clamp-3 text-sm text-white/75">{{ it.summary }}</p>
              <div class="mt-3 flex flex-wrap items-center gap-2">
                <button
                  v-for="t in (it.tags ?? []).slice(0, 4)"
                  :key="t"
                  type="button"
                  class="chip cursor-pointer hover:border-primary/60 hover:text-primary"
                  :class="filters.tag === t ? 'border-primary text-primary' : ''"
                  :aria-label="`Filtrar pela tag ${t}`"
                  data-testid="tag-chip"
                  @click="setFilter({ tag: t })"
                >
                  #{{ t }}
                </button>
                <RouterLink v-if="it.duplicate_count > 0" :to="`/items/${it.item_id}`" class="chip border-accent/50 text-accent" data-testid="dup-chip">
                  <AppIcon name="layers" :size="12" /> +{{ it.duplicate_count }} {{ it.duplicate_count === 1 ? 'duplicado' : 'duplicados' }}
                </RouterLink>
                <a
                  :href="it.url"
                  target="_blank"
                  rel="noopener noreferrer"
                  class="ml-auto inline-flex size-11 items-center justify-center rounded-xl text-muted hover:bg-raised hover:text-white"
                  :aria-label="`Abrir original: ${it.title} (nova aba)`"
                  data-testid="open-original"
                >
                  <AppIcon name="external" :size="18" />
                </a>
              </div>
            </div>
          </li>
        </ul>

        <nav class="mt-6 flex items-center justify-between gap-3" aria-label="Paginação" data-testid="pagination">
          <button type="button" class="btn-ghost" :disabled="filters.page! <= 1 || loading" data-testid="page-prev" @click="setFilter({ page: filters.page! - 1 })">
            <AppIcon name="chevronLeft" :size="16" /> Anterior
          </button>
          <p class="font-mono text-sm text-muted" aria-live="polite">Página {{ filters.page }} de {{ totalPages }}</p>
          <button type="button" class="btn-ghost" :disabled="filters.page! >= totalPages || loading" data-testid="page-next" @click="setFilter({ page: filters.page! + 1 })">
            Próxima <AppIcon name="chevronRight" :size="16" />
          </button>
        </nav>
      </template>
    </section>
  </div>
</template>
