<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api, errorMessage } from '../api/client'
import type { Metrics } from '../api/types'
import AppIcon from '../components/AppIcon.vue'
import AreaChart from '../components/AreaChart.vue'
import BarChart from '../components/BarChart.vue'
import CountUp from '../components/CountUp.vue'
import PageHeader from '../components/PageHeader.vue'
import SegmentedControl from '../components/SegmentedControl.vue'
import StatePanel from '../components/StatePanel.vue'
import { fmtCompact, fmtDateTime, fmtDay, fmtNumber, fmtRelative, fmtScore, kindLabel, shortDay } from '../lib/format'

const SERIES_1 = '#FFD400'
const SERIES_2 = '#4F8DF7'

const days = ref(14)
const dayOptions = [7, 14, 30].map((d) => ({ v: d, l: `${d} dias` }))
const m = ref<Metrics | null>(null)
const loading = ref(true)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    m.value = await api.metrics(days.value)
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}
watch(days, load, { immediate: true })

const perDay = computed(() => m.value?.items_per_day ?? [])
const totals = computed(() => ({
  fetched: perDay.value.reduce((a, d) => a + d.fetched, 0),
  scored: perDay.value.reduce((a, d) => a + d.scored, 0),
}))
const sources = computed(() => m.value?.by_source ?? [])
const maxSourceItems = computed(() => Math.max(1, ...sources.value.map((s) => s.items)))

// Tokens per day split by model kind (chat = nota/dedup LLM, embed = embeddings).
const tokenDays = computed(() => {
  const byDay = new Map<string, { chat: number; embed: number }>()
  for (const d of perDay.value) byDay.set(d.day, { chat: 0, embed: 0 })
  for (const t of m.value?.tokens.per_day ?? []) {
    const row = byDay.get(t.day) ?? { chat: 0, embed: 0 }
    const total = t.prompt_tokens + t.completion_tokens
    if (t.kind === 'embed') row.embed += total
    else row.chat += total
    byDay.set(t.day, row)
  }
  const keys = [...byDay.keys()].sort()
  return { keys, chat: keys.map((k) => byDay.get(k)!.chat), embed: keys.map((k) => byDay.get(k)!.embed) }
})
const models = computed(() => [...new Set((m.value?.tokens.per_day ?? []).map((t) => `${t.kind}: ${t.model}`))])
const statusEntries = computed(() => Object.entries(m.value?.status ?? {}).sort((a, b) => b[1] - a[1]))
const maxStatus = computed(() => Math.max(1, ...statusEntries.value.map(([, v]) => v)))
const failures = computed(() => (m.value ? m.value.failures.fetch_errors + m.value.failures.score_failed : 0))
const fmtPct = (r: number) => `${(r * 100).toFixed(1).replace('.', ',')}%`
const statusLabel: Record<string, string> = { raw: 'aguardando nota', scored: 'com nota', deduped: 'processados', duplicate: 'duplicados', failed: 'falharam' }
</script>

<template>
  <div>
    <PageHeader index="04" section="Métricas" title="Saúde do pipeline" :muted="2">
      <template #meta>últimos <span class="num">{{ days }}</span> dias<span v-if="m" class="max-sm:hidden"> · {{ m.timezone }}</span></template>
      <template #actions>
        <SegmentedControl v-model="days" :options="dayOptions" label="Período" testid="metrics-days" />
        <button type="button" class="btn-icon" :disabled="loading" aria-label="Atualizar métricas" data-testid="metrics-refresh" @click="load">
          <AppIcon name="refresh" :size="16" :class="loading ? 'animate-spin' : ''" />
        </button>
      </template>
    </PageHeader>

    <div v-if="loading && !m" class="mt-8 space-y-6" data-testid="metrics-loading" aria-hidden="true">
      <div class="skeleton h-16 w-3/4" />
      <div class="grid grid-cols-2 gap-4 lg:grid-cols-4"><div v-for="i in 4" :key="i" class="skeleton h-28" /></div>
      <div class="skeleton h-72" />
    </div>
    <StatePanel v-else-if="error" class="mt-8" kind="error" title="Não foi possível carregar as métricas" :detail="error" @retry="load" />

    <div v-else-if="m" class="transition-opacity duration-200" :class="loading ? 'opacity-50' : ''" data-testid="metrics">
      <!-- Data story: one sentence built from the real numbers -->
      <p v-reveal="0" class="max-w-5xl py-10 font-display text-2xl leading-snug font-medium tracking-tight text-white/45 md:py-14 md:text-4xl" data-testid="metrics-story">
        Em <span class="text-white">{{ days }} dias</span> o radar coletou
        <span class="text-primary">{{ fmtNumber(totals.fetched) }} itens</span>, deu nota a
        <span class="text-white">{{ fmtNumber(totals.scored) }}</span> e agrupou
        <span class="text-white">{{ fmtPct(m.dedup.rate) }}</span> como duplicados<template v-if="failures > 0">
          — com <span class="text-accent">{{ fmtNumber(failures) }} {{ failures === 1 ? 'falha' : 'falhas' }}</span> no caminho</template
        ><template v-else> — sem falhas de coleta</template>.
      </p>

      <!-- KPI strip -->
      <dl class="grid grid-cols-2 border-y border-line lg:grid-cols-4">
        <div v-reveal="0" class="py-6 pr-4" data-testid="kpi-fetched">
          <dt class="eyebrow">Coletados</dt>
          <dd class="mt-2 text-4xl font-bold text-primary md:text-6xl"><CountUp :value="totals.fetched" :format="fmtNumber" /></dd>
          <dd class="mt-1 text-xs text-muted">{{ fmtNumber(totals.scored) }} com nota</dd>
        </div>
        <div v-reveal="1" class="border-l border-line py-6 pl-4 md:pl-6" data-testid="kpi-dedup">
          <dt class="eyebrow">Taxa de dedup</dt>
          <dd class="mt-2 text-4xl font-bold md:text-6xl"><CountUp :value="m.dedup.rate * 100" :decimals="1" :format="(n) => `${n.toFixed(1).replace('.', ',')}%`" /></dd>
          <dd class="mt-1 text-xs text-muted">{{ m.dedup.duplicates }} duplicados em {{ m.dedup.clustered }} itens</dd>
        </div>
        <div v-reveal="2" class="border-line py-6 pr-4 max-lg:border-t lg:border-l lg:pl-6" data-testid="kpi-failures">
          <dt class="eyebrow">Falhas</dt>
          <dd class="mt-2 text-4xl font-bold md:text-6xl" :class="failures > 0 ? 'text-accent' : ''"><CountUp :value="failures" :format="fmtNumber" /></dd>
          <dd class="mt-1 text-xs text-muted">{{ m.failures.fetch_errors }} coleta · {{ m.failures.score_failed }} nota</dd>
        </div>
        <div v-reveal="3" class="border-l border-line py-6 pl-4 max-lg:border-t md:pl-6" data-testid="kpi-tokens">
          <dt class="eyebrow">Tokens</dt>
          <dd class="mt-2 text-4xl font-bold md:text-6xl" :title="fmtNumber(m.tokens.total_tokens)"><CountUp :value="m.tokens.total_tokens" :format="fmtCompact" /></dd>
          <dd class="mt-1 text-xs text-muted">{{ fmtNumber(m.tokens.calls) }} chamadas ao Ollama</dd>
        </div>
      </dl>

      <div class="mt-10 grid gap-10 lg:grid-cols-12">
        <!-- Items per day -->
        <section v-reveal="0" class="min-w-0 lg:col-span-8" aria-labelledby="ipd-h">
          <p class="eyebrow">Fig. 1</p>
          <h2 id="ipd-h" class="mt-1 font-display text-2xl font-bold tracking-tight">Itens por dia</h2>
          <p class="mb-4 text-sm text-muted">Coletados (primeira vez vistos) e pontuados pelo LLM.</p>
          <AreaChart
            title="Itens coletados e pontuados por dia"
            :labels="perDay.map((d) => shortDay(d.day))"
            :tooltip-labels="perDay.map((d) => fmtDay(d.day))"
            :series="[
              { name: 'Coletados', color: SERIES_1, values: perDay.map((d) => d.fetched) },
              { name: 'Pontuados', color: SERIES_2, values: perDay.map((d) => d.scored) },
            ]"
            :format="fmtNumber"
            testid="chart-items-per-day"
          />
        </section>

        <!-- Status + dedup -->
        <section v-reveal="1" class="min-w-0 lg:col-span-4" aria-labelledby="dedup-h" data-testid="dedup">
          <p class="eyebrow">Fig. 2</p>
          <h2 id="dedup-h" class="mt-1 font-display text-2xl font-bold tracking-tight">Dedup e clusters</h2>
          <dl class="mt-4 grid grid-cols-2 border-y border-line">
            <div class="py-4 pr-3">
              <dt class="eyebrow">Clusters</dt>
              <dd class="num mt-1 text-3xl font-bold">{{ fmtNumber(m.dedup.clusters) }}</dd>
            </div>
            <div class="border-l border-line py-4 pl-4">
              <dt class="eyebrow">Com 2+ itens</dt>
              <dd class="num mt-1 text-3xl font-bold text-accent">{{ fmtNumber(m.dedup.multi_item_clusters) }}</dd>
            </div>
          </dl>
          <h3 class="mt-6 eyebrow">Itens por status</h3>
          <ul class="mt-3 space-y-3 text-sm">
            <li v-for="([k, v], i) in statusEntries" :key="k">
              <div class="flex justify-between gap-3"><span class="text-white/85">{{ statusLabel[k] ?? k }}</span><span class="num">{{ fmtNumber(v) }}</span></div>
              <div class="mt-1 h-1 overflow-hidden rounded-full bg-white/10" aria-hidden="true">
                <div class="meter-fill h-full" :class="k === 'failed' ? 'bg-danger' : k === 'duplicate' ? 'bg-accent' : 'bg-white/70'" :style="{ width: `${(v / maxStatus) * 100}%`, '--d': `${i * 60}ms` }" />
              </div>
            </li>
            <li v-if="statusEntries.length === 0" class="text-muted">Sem itens no período.</li>
          </ul>
        </section>

        <!-- By source -->
        <section v-reveal="0" class="min-w-0 lg:col-span-6" aria-labelledby="src-h" data-testid="by-source">
          <p class="eyebrow">Fig. 3</p>
          <h2 id="src-h" class="mt-1 font-display text-2xl font-bold tracking-tight">Por fonte</h2>
          <p v-if="sources.length === 0" class="mt-3 text-sm text-muted">Nenhuma fonte cadastrada.</p>
          <ul v-else class="mt-4 border-t border-line">
            <li v-for="(s, i) in sources" :key="s.source_id" class="border-b border-line py-4">
              <div class="flex flex-wrap items-baseline justify-between gap-x-3 text-sm">
                <span class="font-medium break-words">
                  {{ s.name }}
                  <span class="text-xs text-muted">{{ [s.name !== kindLabel[s.kind] ? kindLabel[s.kind] : '', s.enabled ? '' : 'desativada'].filter(Boolean).join(' · ') }}</span>
                </span>
                <span class="font-mono text-xs text-muted">média <span class="text-primary">{{ fmtScore(s.avg_score) }}</span></span>
              </div>
              <div class="mt-2 flex items-center gap-3">
                <div class="h-2 flex-1 overflow-hidden rounded-full bg-white/10" aria-hidden="true">
                  <div class="meter-fill h-full rounded-full bg-primary" :style="{ width: `${(s.items / maxSourceItems) * 100}%`, '--d': `${i * 70}ms` }" />
                </div>
                <span class="num w-24 shrink-0 text-right text-sm">{{ fmtNumber(s.items) }} itens</span>
              </div>
              <p class="mt-1 text-xs text-muted">
                {{ s.feed_items }} no feed · coletas {{ s.fetch_ok }} ok /
                <span :class="s.fetch_errors ? 'text-accent' : ''">{{ s.fetch_errors }} erro</span>
                · última {{ fmtRelative(s.last_fetch_at) }}
              </p>
            </li>
          </ul>
        </section>

        <!-- Failures -->
        <section v-reveal="1" class="min-w-0 lg:col-span-6" aria-labelledby="fail-h" data-testid="failures">
          <p class="eyebrow">Fig. 4</p>
          <h2 id="fail-h" class="mt-1 font-display text-2xl font-bold tracking-tight">Falhas recentes</h2>
          <dl class="mt-4 grid grid-cols-3 border-y border-line">
            <div class="py-4 pr-2">
              <dt class="eyebrow">Coleta</dt>
              <dd class="num mt-1 text-2xl font-bold">{{ m.failures.fetch_errors }}</dd>
            </div>
            <div class="border-l border-line px-3 py-4">
              <dt class="eyebrow">Nota · desistiu</dt>
              <dd class="num mt-1 text-2xl font-bold">{{ m.failures.score_failed }}</dd>
            </div>
            <div class="border-l border-line py-4 pl-3">
              <dt class="eyebrow">Nota · retentando</dt>
              <dd class="num mt-1 text-2xl font-bold">{{ m.failures.score_retries }}</dd>
            </div>
          </dl>
          <p v-if="!m.failures.recent?.length" class="mt-4 flex items-center gap-2 text-sm text-muted"><AppIcon name="check" :size="16" class="text-ok" /> Nenhuma falha de coleta no período.</p>
          <ul v-else class="mt-4 max-h-72 space-y-2 overflow-y-auto pr-1" tabindex="0" aria-label="Lista de falhas recentes">
            <li v-for="(f, i) in m.failures.recent" :key="i" class="border-l-2 border-accent bg-accent/5 py-3 pr-3 pl-4">
              <p class="flex flex-wrap justify-between gap-x-3 text-sm">
                <span class="flex items-center gap-1.5 font-medium"><AppIcon name="alert" :size="14" class="text-accent" />{{ f.source_name }}</span>
                <time class="num text-xs text-muted" :datetime="f.at">{{ fmtDateTime(f.at) }}</time>
              </p>
              <p class="mt-1 font-mono text-xs break-all text-white/75">{{ f.error }}</p>
            </li>
          </ul>
        </section>

        <!-- Tokens -->
        <section v-reveal="0" class="min-w-0 lg:col-span-12" aria-labelledby="tok-h">
          <div class="flex flex-wrap items-end justify-between gap-2">
            <div>
              <p class="eyebrow">Fig. 5</p>
              <h2 id="tok-h" class="mt-1 font-display text-2xl font-bold tracking-tight">Tokens por dia</h2>
            </div>
            <p class="font-mono text-xs text-muted">prompt {{ fmtNumber(m.tokens.prompt_tokens) }} · resposta {{ fmtNumber(m.tokens.completion_tokens) }}</p>
          </div>
          <p class="mb-4 text-sm text-muted">Ollama local — custo em tokens, não em dinheiro. <span v-if="models.length">Modelos: {{ models.join(', ') }}.</span></p>
          <BarChart
            title="Tokens por dia, LLM e embeddings"
            :labels="tokenDays.keys.map(shortDay)"
            :tooltip-labels="tokenDays.keys.map(fmtDay)"
            :series="[
              { name: 'LLM (chat)', color: SERIES_1, values: tokenDays.chat },
              { name: 'Embeddings', color: SERIES_2, values: tokenDays.embed },
            ]"
            :format="fmtCompact"
            :height="220"
            testid="chart-tokens"
          />
        </section>
      </div>
    </div>
  </div>
</template>
