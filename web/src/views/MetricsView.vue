<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api, errorMessage } from '../api/client'
import type { Metrics } from '../api/types'
import AppIcon from '../components/AppIcon.vue'
import BarChart from '../components/BarChart.vue'
import StatePanel from '../components/StatePanel.vue'
import { fmtCompact, fmtDateTime, fmtDay, fmtNumber, fmtRelative, fmtScore, kindLabel, shortDay } from '../lib/format'

const SERIES_1 = '#FFD400'
const SERIES_2 = '#4F8DF7'

const days = ref(14)
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
const statusLabel: Record<string, string> = { raw: 'aguardando nota', scored: 'com nota', deduped: 'processados', duplicate: 'duplicados', failed: 'falharam' }
</script>

<template>
  <div class="mx-auto max-w-7xl">
    <header class="flex flex-wrap items-end justify-between gap-4">
      <div>
        <p class="eyebrow">Radar · métricas</p>
        <h1 class="mt-1 font-display text-3xl font-bold md:text-4xl">Saúde do pipeline</h1>
        <p class="mt-1 text-sm text-muted">
          Últimos {{ days }} dias<span v-if="m"> · fuso {{ m.timezone }} · gerado {{ fmtRelative(m.generated_at) }}</span>
        </p>
      </div>
      <div class="flex items-center gap-2">
        <div class="flex rounded-xl border border-line bg-surface p-1" role="group" aria-label="Período">
          <button
            v-for="d in [7, 14, 30]"
            :key="d"
            type="button"
            class="min-h-10 min-w-12 cursor-pointer rounded-lg px-3 font-mono text-sm transition-colors duration-200"
            :class="days === d ? 'bg-primary text-ink' : 'text-white/80 hover:text-white'"
            :aria-pressed="days === d"
            :data-testid="`metrics-days-${d}`"
            @click="days = d"
          >
            {{ d }}d
          </button>
        </div>
        <button type="button" class="btn-icon" :disabled="loading" aria-label="Atualizar métricas" data-testid="metrics-refresh" @click="load">
          <AppIcon name="refresh" :size="16" :class="loading ? 'animate-spin' : ''" />
        </button>
      </div>
    </header>

    <div v-if="loading && !m" class="mt-5 grid grid-cols-2 gap-4 lg:grid-cols-4" data-testid="metrics-loading">
      <div v-for="i in 4" :key="i" class="card skeleton h-28" />
      <div class="card skeleton col-span-2 h-72 lg:col-span-4" />
    </div>
    <StatePanel v-else-if="error" class="mt-5" kind="error" title="Não foi possível carregar as métricas" :detail="error" @retry="load" />

    <div v-else-if="m" class="mt-5 grid grid-cols-2 gap-4 transition-opacity duration-200 lg:grid-cols-4" :class="loading ? 'opacity-60' : ''" data-testid="metrics">
      <!-- KPI tiles -->
      <div class="card p-4 md:p-5" data-testid="kpi-fetched">
        <p class="eyebrow">Coletados</p>
        <p class="mt-2 font-mono text-3xl font-bold text-primary md:text-4xl">{{ fmtNumber(totals.fetched) }}</p>
        <p class="mt-1 text-xs text-muted">{{ fmtNumber(totals.scored) }} com nota</p>
      </div>
      <div class="card p-4 md:p-5" data-testid="kpi-dedup">
        <p class="eyebrow">Taxa de dedup</p>
        <p class="mt-2 font-mono text-3xl font-bold md:text-4xl">{{ (m.dedup.rate * 100).toFixed(1).replace('.', ',') }}%</p>
        <p class="mt-1 text-xs text-muted">{{ m.dedup.duplicates }} duplicados em {{ m.dedup.clustered }} itens</p>
      </div>
      <div class="card p-4 md:p-5" data-testid="kpi-failures">
        <p class="eyebrow">Falhas</p>
        <p class="mt-2 font-mono text-3xl font-bold md:text-4xl" :class="m.failures.fetch_errors + m.failures.score_failed > 0 ? 'text-accent' : ''">
          {{ fmtNumber(m.failures.fetch_errors + m.failures.score_failed) }}
        </p>
        <p class="mt-1 text-xs text-muted">{{ m.failures.fetch_errors }} coleta · {{ m.failures.score_failed }} nota</p>
      </div>
      <div class="card p-4 md:p-5" data-testid="kpi-tokens">
        <p class="eyebrow">Tokens</p>
        <p class="mt-2 font-mono text-3xl font-bold md:text-4xl" :title="fmtNumber(m.tokens.total_tokens)">{{ fmtCompact(m.tokens.total_tokens) }}</p>
        <p class="mt-1 text-xs text-muted">{{ fmtNumber(m.tokens.calls) }} chamadas ao Ollama</p>
      </div>

      <!-- Items per day -->
      <section class="card col-span-2 p-4 md:p-5 lg:col-span-3" aria-labelledby="ipd-h">
        <h2 id="ipd-h" class="font-display font-semibold">Itens por dia</h2>
        <p class="mb-3 text-xs text-muted">Coletados (primeira vez vistos) e pontuados pelo LLM.</p>
        <BarChart
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
      <section class="card col-span-2 p-4 md:p-5 lg:col-span-1" aria-labelledby="dedup-h" data-testid="dedup">
        <h2 id="dedup-h" class="font-display font-semibold">Dedup e clusters</h2>
        <dl class="mt-3 grid grid-cols-2 gap-3 lg:grid-cols-1">
          <div class="rounded-xl bg-raised p-3">
            <dt class="text-xs text-muted">Clusters</dt>
            <dd class="font-mono text-2xl font-bold">{{ fmtNumber(m.dedup.clusters) }}</dd>
          </div>
          <div class="rounded-xl bg-raised p-3">
            <dt class="text-xs text-muted">Com 2+ itens</dt>
            <dd class="font-mono text-2xl font-bold">{{ fmtNumber(m.dedup.multi_item_clusters) }}</dd>
          </div>
        </dl>
        <h3 class="mt-4 eyebrow">Itens por status</h3>
        <ul class="mt-2 space-y-1 text-sm">
          <li v-for="[k, v] in statusEntries" :key="k" class="flex justify-between gap-3">
            <span class="text-white/85">{{ statusLabel[k] ?? k }}</span><span class="font-mono">{{ fmtNumber(v) }}</span>
          </li>
          <li v-if="statusEntries.length === 0" class="text-muted">Sem itens no período.</li>
        </ul>
      </section>

      <!-- By source -->
      <section class="card col-span-2 p-4 md:p-5" aria-labelledby="src-h" data-testid="by-source">
        <h2 id="src-h" class="font-display font-semibold">Por fonte</h2>
        <p v-if="sources.length === 0" class="mt-3 text-sm text-muted">Nenhuma fonte cadastrada.</p>
        <ul v-else class="mt-3 space-y-4">
          <li v-for="s in sources" :key="s.source_id">
            <div class="flex flex-wrap items-baseline justify-between gap-x-3 text-sm">
              <span class="font-medium break-words">
                {{ s.name }} <span class="text-xs text-muted">{{ kindLabel[s.kind] }}{{ s.enabled ? '' : ' · desativada' }}</span>
              </span>
              <span class="font-mono text-xs text-muted">média <span class="text-primary">{{ fmtScore(s.avg_score) }}</span></span>
            </div>
            <div class="mt-1.5 flex items-center gap-3">
              <div class="h-2.5 flex-1 overflow-hidden rounded-full bg-raised" aria-hidden="true">
                <div class="h-full rounded-full bg-primary" :style="{ width: `${(s.items / maxSourceItems) * 100}%` }" />
              </div>
              <span class="w-24 shrink-0 text-right font-mono text-sm">{{ fmtNumber(s.items) }} itens</span>
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
      <section class="card col-span-2 p-4 md:p-5" aria-labelledby="fail-h" data-testid="failures">
        <h2 id="fail-h" class="font-display font-semibold">Falhas recentes</h2>
        <dl class="mt-3 grid grid-cols-3 gap-3">
          <div class="rounded-xl bg-raised p-3">
            <dt class="text-xs text-muted">Coleta</dt>
            <dd class="font-mono text-xl font-bold">{{ m.failures.fetch_errors }}</dd>
          </div>
          <div class="rounded-xl bg-raised p-3">
            <dt class="text-xs text-muted">Nota (desistiu)</dt>
            <dd class="font-mono text-xl font-bold">{{ m.failures.score_failed }}</dd>
          </div>
          <div class="rounded-xl bg-raised p-3">
            <dt class="text-xs text-muted">Nota (retentando)</dt>
            <dd class="font-mono text-xl font-bold">{{ m.failures.score_retries }}</dd>
          </div>
        </dl>
        <p v-if="!m.failures.recent?.length" class="mt-4 flex items-center gap-2 text-sm text-muted"><AppIcon name="check" :size="16" class="text-ok" /> Nenhuma falha de coleta no período.</p>
        <ul v-else class="mt-4 max-h-64 space-y-2 overflow-y-auto pr-1">
          <li v-for="(f, i) in m.failures.recent" :key="i" class="rounded-xl border border-accent/30 bg-accent/5 p-3">
            <p class="flex flex-wrap justify-between gap-x-3 text-sm">
              <span class="flex items-center gap-1.5 font-medium"><AppIcon name="alert" :size="14" class="text-accent" />{{ f.source_name }}</span>
              <time class="text-xs text-muted" :datetime="f.at">{{ fmtDateTime(f.at) }}</time>
            </p>
            <p class="mt-1 font-mono text-xs break-all text-white/75">{{ f.error }}</p>
          </li>
        </ul>
      </section>

      <!-- Tokens -->
      <section class="card col-span-2 p-4 md:p-5 lg:col-span-4" aria-labelledby="tok-h">
        <div class="flex flex-wrap items-baseline justify-between gap-2">
          <h2 id="tok-h" class="font-display font-semibold">Tokens por dia</h2>
          <p class="font-mono text-xs text-muted">
            prompt {{ fmtNumber(m.tokens.prompt_tokens) }} · resposta {{ fmtNumber(m.tokens.completion_tokens) }}
          </p>
        </div>
        <p class="mb-3 text-xs text-muted">Ollama local — custo em tokens, não em dinheiro. <span v-if="models.length">Modelos: {{ models.join(', ') }}.</span></p>
        <BarChart
          title="Tokens por dia, LLM e embeddings"
          :labels="tokenDays.keys.map(shortDay)"
          :tooltip-labels="tokenDays.keys.map(fmtDay)"
          :series="[
            { name: 'LLM (chat)', color: SERIES_1, values: tokenDays.chat },
            { name: 'Embeddings', color: SERIES_2, values: tokenDays.embed },
          ]"
          :format="fmtCompact"
          :height="200"
          testid="chart-tokens"
        />
      </section>
    </div>
  </div>
</template>
