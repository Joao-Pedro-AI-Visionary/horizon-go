<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { ApiError, api, errorMessage } from '../api/client'
import type { CollectReport, FeedItem, FeedPage, FeedParams, Source } from '../api/types'
import AppIcon from '../components/AppIcon.vue'
import CountUp from '../components/CountUp.vue'
import PageHeader from '../components/PageHeader.vue'
import ScoreBadge from '../components/ScoreBadge.vue'
import StatePanel from '../components/StatePanel.vue'
import { fmtNumber, fmtRelative, fmtScore, hostOf } from '../lib/format'
import { staggerDelay } from '../lib/motion'
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

const periods = [
  { v: '24h', l: '24h' },
  { v: '7d', l: '7 dias' },
  { v: '30d', l: '30 dias' },
  { v: 'all', l: 'Tudo' },
] as const
const periodLabel: Record<string, string> = { '24h': 'últimas 24h', '7d': '7 dias', '30d': '30 dias', all: 'todo o período' }
const sorts = [
  { v: 'score', l: 'Maior nota' },
  { v: 'recent', l: 'Recentes' },
] as const

// On mobile the filters below the search are collapsed behind a toggle
// (progressive disclosure); from 640px they are always visible.
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
const thresholdLoaded = ref(false)
onMounted(async () => {
  load()
  const [s, st] = await Promise.allSettled([api.sources(), api.settings()])
  if (s.status === 'fulfilled') sources.value = s.value.sources ?? []
  if (st.status === 'fulfilled') {
    threshold.value = st.value.score_threshold
    thresholdLoaded.value = true
  }
})
onUnmounted(() => ctrl?.abort())

const items = computed<FeedItem[]>(() => page.value?.items ?? [])
// Page 1 opens with a lead story (the top item); the rest is a numbered list.
const lead = computed(() => (filters.value.page === 1 ? items.value[0] : undefined))
const rest = computed(() => (lead.value ? items.value.slice(1) : items.value))
const offset = computed(() => ((filters.value.page ?? 1) - 1) * PAGE_SIZE)
const totalPages = computed(() => (page.value ? Math.max(1, Math.ceil(page.value.total / page.value.page_size)) : 1))
const enabledSources = computed(() => sources.value.filter((s) => s.enabled).length)
const hasFilters = computed(() => {
  const f = filters.value
  return !!(f.q || f.tag || f.source_id || f.min_score || f.period !== '7d')
})
const pad2 = (n: number) => String(n).padStart(2, '0')

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
const reportLabels = { fetched: 'Coletados', published: 'Publicados', duplicates: 'Já vistos', invalid: 'Inválidos' } as const
</script>

<template>
  <div>
    <PageHeader index="01" section="Feed" title="O que importa hoje" :muted="1" lead="Notícias de HN, Reddit e RSS ranqueadas pela nota do LLM local — uma por cluster de duplicados.">
      <template #meta>{{ periodLabel[filters.period ?? '7d'] }} · ordem {{ filters.sort === 'recent' ? 'recente' : 'nota' }}</template>
      <template #actions>
        <button type="button" class="btn-primary group" :disabled="running" :aria-busy="running" data-testid="run-pipeline" @click="runPipeline">
          <AppIcon :name="running ? 'refresh' : 'play'" :size="16" :class="running ? 'animate-spin' : ''" />
          {{ running ? 'Coletando fontes…' : 'Rodar pipeline' }}
        </button>
      </template>
    </PageHeader>

    <!-- Corner stats (real numbers from /api/feed, /api/sources, /api/settings) -->
    <dl class="grid grid-cols-3 border-b border-line" aria-label="Resumo do feed">
      <div v-reveal="0" class="py-5 pr-3 md:py-7">
        <dt class="eyebrow">Itens no feed</dt>
        <dd class="mt-2 text-3xl font-bold md:text-5xl" data-testid="feed-total">
          <CountUp v-if="page" :value="page.total" :format="fmtNumber" /><span v-else class="num text-muted">—</span>
        </dd>
      </div>
      <div v-reveal="1" class="border-l border-line py-5 pr-3 pl-4 md:py-7 md:pl-6">
        <dt class="eyebrow">Fontes ativas</dt>
        <dd class="mt-2 text-3xl font-bold md:text-5xl"><CountUp :value="enabledSources" /><span class="num text-lg text-muted md:text-2xl">/{{ sources.length }}</span></dd>
      </div>
      <div v-reveal="2" class="border-l border-line py-5 pl-4 md:py-7 md:pl-6">
        <dt class="eyebrow">Nota de corte</dt>
        <dd class="mt-2 text-3xl font-bold text-primary md:text-5xl"><span class="num">{{ thresholdLoaded ? fmtScore(threshold) : '—' }}</span></dd>
      </div>
    </dl>

    <!-- Last pipeline report -->
    <Transition name="drop">
      <section v-if="report" class="card mt-6 p-5" aria-labelledby="report-title" data-testid="pipeline-report">
        <div class="flex flex-wrap items-start justify-between gap-2">
          <div>
            <p class="eyebrow text-primary">Relatório da coleta</p>
            <h2 id="report-title" class="mt-1 font-display text-xl font-semibold">Última coleta <span class="num text-xs font-normal text-muted">{{ report.run_id }}</span></h2>
          </div>
          <button type="button" class="btn-icon" aria-label="Fechar relatório" data-testid="close-report" @click="report = null">
            <AppIcon name="close" :size="16" />
          </button>
        </div>
        <dl class="mt-4 grid grid-cols-2 border-t border-line sm:grid-cols-4">
          <div v-for="(k, i) in (['fetched', 'published', 'duplicates', 'invalid'] as const)" :key="k" class="border-line py-3 pr-3" :class="i % 2 === 1 ? 'border-l pl-4' : i === 2 ? 'sm:border-l sm:pl-4' : ''">
            <dt class="eyebrow">{{ reportLabels[k] }}</dt>
            <dd class="mt-1 text-3xl font-bold"><CountUp :value="report[k]" /></dd>
          </div>
        </dl>
        <ul class="mt-3 flex flex-wrap gap-2">
          <li v-for="s in report.sources" :key="s.source_id" class="chip" :class="s.status === 'ok' ? '' : 'border-danger/50 text-danger'" :title="s.error || undefined">
            {{ s.source_name }} · {{ s.status === 'ok' ? `${s.item_count} itens` : 'erro' }}
          </li>
        </ul>
        <p class="mt-3 text-xs text-muted">Os itens novos entram no feed depois que o worker dá a nota e faz o dedup.</p>
      </section>
    </Transition>

    <!-- Filters -->
    <section class="mt-6 flex flex-col gap-3" aria-label="Filtros do feed">
      <div class="grid gap-3 sm:grid-cols-2 lg:flex lg:items-end">
        <div class="sm:col-span-2 lg:flex-1">
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
        <div class="contents" :class="filtersOpen ? '' : 'max-sm:hidden'">
          <div class="max-sm:order-1">
            <span id="f-period-l" class="label">Período</span>
            <div class="seg w-full lg:w-auto" role="group" aria-labelledby="f-period-l">
              <button
                v-for="p in periods"
                :key="p.v"
                type="button"
                class="seg-btn"
                :aria-pressed="filters.period === p.v"
                :data-testid="`filter-period-${p.v}`"
                @click="setFilter({ period: p.v })"
              >
                {{ p.l }}
              </button>
            </div>
          </div>
          <div class="max-sm:order-1">
            <span id="f-sort-l" class="label">Ordem</span>
            <div class="seg w-full lg:w-auto" role="group" aria-labelledby="f-sort-l">
              <button
                v-for="s in sorts"
                :key="s.v"
                type="button"
                class="seg-btn"
                :aria-pressed="filters.sort === s.v"
                :data-testid="`filter-sort-${s.v}`"
                @click="setFilter({ sort: s.v })"
              >
                {{ s.l }}
              </button>
            </div>
          </div>
        </div>
      </div>
      <div id="feed-more-filters" class="grid gap-3 sm:grid-cols-2 lg:grid-cols-12 lg:items-end" :class="filtersOpen ? '' : 'max-sm:hidden'">
        <div class="lg:col-span-3">
          <label for="f-source" class="label">Fonte</label>
          <select id="f-source" class="field cursor-pointer" :value="filters.source_id ?? ''" data-testid="filter-source" @change="setFilter({ source_id: ($event.target as HTMLSelectElement).value || undefined })">
            <option value="">Todas</option>
            <option v-for="s in sources" :key="s.id" :value="s.id">{{ s.name }}</option>
          </select>
        </div>
        <div class="lg:col-span-3">
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
        <div class="sm:col-span-2 lg:col-span-6">
          <div class="flex items-center justify-between">
            <label for="f-min" class="label">Nota mínima</label>
            <span class="num -mt-1 text-sm text-primary" aria-hidden="true">{{ minScore.toFixed(1) }}</span>
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
            <button v-if="hasFilters" type="button" class="btn-ghost shrink-0" data-testid="clear-filters" @click="router.replace({ query: {} })">
              <AppIcon name="close" :size="14" /> Limpar
            </button>
          </div>
        </div>
      </div>
    </section>

    <!-- Results -->
    <section class="mt-8" aria-label="Itens do feed" :aria-busy="loading">
      <div v-if="loading && !page" data-testid="feed-loading" aria-hidden="true">
        <div class="grid gap-6 border-t border-line py-8 lg:grid-cols-12">
          <div class="skeleton h-28 w-40 lg:col-span-3" />
          <div class="space-y-3 lg:col-span-9">
            <div class="skeleton h-10 w-3/4" />
            <div class="skeleton h-5 w-full" />
            <div class="skeleton h-5 w-2/3" />
          </div>
        </div>
        <div v-for="i in 5" :key="i" class="flex gap-5 border-t border-line py-6">
          <div class="skeleton h-10 w-16 shrink-0" />
          <div class="flex-1 space-y-3">
            <div class="skeleton h-6 w-2/3" />
            <div class="skeleton h-4 w-full" />
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
        <div class="transition-opacity duration-200" :class="loading ? 'opacity-50' : ''" data-testid="feed-list">
          <!-- Lead story -->
          <article v-if="lead" :key="`lead-${lead.item_id}`" v-reveal="0" class="grid gap-6 border-t-2 border-primary py-8 md:py-10 lg:grid-cols-12" data-testid="feed-card">
            <div class="flex items-start justify-between gap-4 lg:col-span-3 lg:flex-col">
              <div>
                <p class="eyebrow mb-3">Nº 01 · manchete</p>
                <ScoreBadge :score="lead.score" :threshold="threshold" size="xl" delay="250ms" />
              </div>
            </div>
            <div class="min-w-0 lg:col-span-9">
              <p class="flex flex-wrap items-center gap-x-2 font-mono text-xs text-muted">
                <span class="text-white/85">{{ lead.source_name }}</span><span aria-hidden="true">/</span><span>{{ hostOf(lead.url) }}</span><span aria-hidden="true">/</span>
                <time :datetime="lead.published_at ?? lead.projected_at">{{ fmtRelative(lead.published_at ?? lead.projected_at) }}</time>
              </p>
              <h2 class="display-lg mt-3 break-words">
                <RouterLink :to="`/items/${lead.item_id}`" class="decoration-primary decoration-2 underline-offset-[6px] transition-colors duration-200 hover:text-primary" data-testid="open-item">{{ lead.title }}</RouterLink>
              </h2>
              <p v-if="lead.summary" class="mt-4 max-w-3xl text-lg text-white/80">{{ lead.summary }}</p>
              <blockquote v-if="lead.score_reason" class="mt-5 max-w-3xl border-l-2 border-primary pl-4 text-sm text-white/70">
                <span class="eyebrow mb-1 block text-primary">Por que essa nota</span>{{ lead.score_reason }}
              </blockquote>
              <div class="mt-5 flex flex-wrap items-center gap-2">
                <button
                  v-for="t in (lead.tags ?? []).slice(0, 5)"
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
                <RouterLink v-if="lead.duplicate_count > 0" :to="`/items/${lead.item_id}`" class="chip border-accent/50 text-accent" data-testid="dup-chip">
                  <AppIcon name="layers" :size="12" /> +{{ lead.duplicate_count }} {{ lead.duplicate_count === 1 ? 'duplicado' : 'duplicados' }}
                </RouterLink>
                <a
                  :href="lead.url"
                  target="_blank"
                  rel="noopener noreferrer"
                  class="btn-ghost group ml-auto"
                  :aria-label="`Abrir original: ${lead.title} (nova aba)`"
                  data-testid="open-original"
                >
                  Original <AppIcon name="external" :size="16" class="nudge" />
                </a>
              </div>
            </div>
          </article>

          <!-- Numbered list -->
          <ol class="border-t border-line">
            <li
              v-for="(it, i) in rest"
              :key="it.item_id"
              v-reveal="i"
              class="group/row grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 border-b border-line py-6 transition-colors duration-200 hover:bg-white/[0.02] md:grid-cols-[3rem_5rem_1fr_auto] md:gap-x-6 md:py-7"
              data-testid="feed-card"
            >
              <span class="num hidden pt-1 text-sm text-muted md:block" aria-hidden="true">{{ pad2(offset + i + (lead ? 2 : 1)) }}</span>
              <ScoreBadge :score="it.score" :threshold="threshold" size="md" :delay="staggerDelay(i)" />
              <div class="min-w-0">
                <p class="flex flex-wrap items-center gap-x-2 font-mono text-xs text-muted">
                  <span class="text-white/85">{{ it.source_name }}</span><span aria-hidden="true">/</span><span class="break-all">{{ hostOf(it.url) }}</span><span aria-hidden="true">/</span>
                  <time :datetime="it.published_at ?? it.projected_at">{{ fmtRelative(it.published_at ?? it.projected_at) }}</time>
                </p>
                <h2 class="mt-1.5 font-display text-xl leading-snug font-semibold tracking-tight break-words md:text-2xl">
                  <RouterLink :to="`/items/${it.item_id}`" class="transition-colors duration-200 hover:text-primary" data-testid="open-item">{{ it.title }}</RouterLink>
                </h2>
                <p v-if="it.summary" class="mt-2 line-clamp-2 max-w-3xl text-sm text-white/70">{{ it.summary }}</p>
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
                </div>
              </div>
              <a
                :href="it.url"
                target="_blank"
                rel="noopener noreferrer"
                class="col-start-2 inline-flex size-11 items-center justify-center justify-self-end rounded-md border border-transparent text-muted transition-colors duration-200 hover:border-line hover:text-white md:col-start-auto md:self-start"
                :aria-label="`Abrir original: ${it.title} (nova aba)`"
                data-testid="open-original"
              >
                <AppIcon name="external" :size="18" />
              </a>
            </li>
          </ol>
        </div>

        <nav class="mt-8 grid grid-cols-[1fr_auto_1fr] items-center gap-3" aria-label="Paginação" data-testid="pagination">
          <button type="button" class="btn-ghost group justify-self-start" :disabled="filters.page! <= 1 || loading" data-testid="page-prev" @click="setFilter({ page: filters.page! - 1 })">
            <AppIcon name="chevronLeft" :size="16" /> Anterior
          </button>
          <p class="num text-sm text-muted" aria-live="polite">
            <span class="sr-only">Página {{ filters.page }} de {{ totalPages }}</span>
            <span aria-hidden="true"><span class="text-xl text-white">{{ pad2(filters.page ?? 1) }}</span> / {{ pad2(totalPages) }}</span>
          </p>
          <button type="button" class="btn-ghost group justify-self-end" :disabled="filters.page! >= totalPages || loading" data-testid="page-next" @click="setFilter({ page: filters.page! + 1 })">
            Próxima <AppIcon name="chevronRight" :size="16" class="nudge" />
          </button>
        </nav>
      </template>
    </section>
  </div>
</template>
