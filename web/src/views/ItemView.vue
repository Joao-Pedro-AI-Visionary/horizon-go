<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { api, errorMessage } from '../api/client'
import type { ItemDetail } from '../api/types'
import AppIcon from '../components/AppIcon.vue'
import ScoreBadge from '../components/ScoreBadge.vue'
import StatePanel from '../components/StatePanel.vue'
import { fmtDateTime, fmtScore, hostOf, kindLabel } from '../lib/format'

const route = useRoute()
const router = useRouter()
const item = ref<ItemDetail | null>(null)
const loading = ref(true)
const error = ref('')
const notFound = ref(false)
let ctrl: AbortController | undefined

async function load() {
  ctrl?.abort()
  ctrl = new AbortController()
  loading.value = true
  error.value = ''
  notFound.value = false
  try {
    item.value = await api.item(Number(route.params.id), ctrl.signal)
  } catch (err) {
    if ((err as Error).name === 'AbortError') return
    notFound.value = (err as { status?: number }).status === 404
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}
watch(() => route.params.id, load, { immediate: true })
onUnmounted(() => ctrl?.abort())

const statusLabel: Record<string, string> = { raw: 'Aguardando nota', scored: 'Com nota', deduped: 'Processado', duplicate: 'Duplicado', failed: 'Falhou' }
const duplicates = computed(() => item.value?.duplicates ?? [])

function back() {
  if (window.history.state?.back) router.back()
  else router.push('/')
}
</script>

<template>
  <div class="mx-auto max-w-6xl">
    <button type="button" class="btn-ghost" data-testid="back" @click="back">
      <AppIcon name="back" :size="16" /> Voltar
    </button>

    <div v-if="loading && !item" class="mt-5 grid gap-4 lg:grid-cols-3" data-testid="item-loading">
      <div class="card space-y-3 p-6 lg:col-span-2">
        <div class="skeleton h-8 w-3/4" />
        <div class="skeleton h-4 w-full" />
        <div class="skeleton h-4 w-5/6" />
      </div>
      <div class="card skeleton h-48" />
    </div>
    <StatePanel v-else-if="notFound" class="mt-5" kind="empty" title="Item não encontrado" detail="Ele pode ter sido removido junto com a fonte." testid="item-not-found">
      <RouterLink to="/" class="btn-primary" data-testid="go-feed">Ir para o feed</RouterLink>
    </StatePanel>
    <StatePanel v-else-if="error" class="mt-5" kind="error" title="Não foi possível carregar o item" :detail="error" @retry="load" />

    <div v-else-if="item" class="mt-5 grid gap-4 lg:grid-cols-3">
      <!-- Main item -->
      <article class="card p-5 md:p-7 lg:col-span-2" data-testid="item-detail">
        <div class="flex items-start gap-4">
          <ScoreBadge :score="item.score" size="lg" />
          <div class="min-w-0 flex-1">
            <p class="flex flex-wrap items-center gap-2 text-xs text-muted">
              <span class="chip">{{ kindLabel[item.source_kind] ?? item.source_kind }}</span>
              <span class="font-medium text-white/85">{{ item.source_name }}</span>
              <span v-if="item.author">· por {{ item.author }}</span>
            </p>
            <h1 class="mt-2 font-display text-2xl leading-tight font-bold break-words md:text-3xl">{{ item.title }}</h1>
          </div>
        </div>

        <a :href="item.url" target="_blank" rel="noopener noreferrer" class="btn-primary mt-5 w-full sm:w-auto" data-testid="item-original">
          <AppIcon name="external" :size="16" /> Abrir original em {{ hostOf(item.url) }}
        </a>

        <section v-if="item.summary" class="mt-6" aria-labelledby="sum-h">
          <h2 id="sum-h" class="eyebrow">Resumo</h2>
          <p class="mt-2 text-white/90">{{ item.summary }}</p>
        </section>

        <section class="mt-6 rounded-xl border-l-4 border-primary bg-raised p-4" aria-labelledby="reason-h">
          <h2 id="reason-h" class="eyebrow">Por que essa nota</h2>
          <p class="mt-2 text-white/90">{{ item.score_reason || 'O item ainda não recebeu nota do LLM.' }}</p>
        </section>

        <section v-if="item.tags?.length" class="mt-6" aria-labelledby="tags-h">
          <h2 id="tags-h" class="eyebrow">Tags</h2>
          <ul class="mt-2 flex flex-wrap gap-2">
            <li v-for="t in item.tags" :key="t">
              <RouterLink :to="{ path: '/', query: { tag: t, period: 'all' } }" class="chip hover:border-primary/60 hover:text-primary" data-testid="item-tag">#{{ t }}</RouterLink>
            </li>
          </ul>
        </section>

        <section v-if="item.content" class="mt-6" aria-labelledby="content-h">
          <h2 id="content-h" class="eyebrow">Conteúdo coletado</h2>
          <p class="mt-2 max-h-72 overflow-y-auto pr-2 text-sm whitespace-pre-line text-white/70 break-words">{{ item.content }}</p>
        </section>
      </article>

      <!-- Side: meta + cluster -->
      <div class="flex flex-col gap-4">
        <section class="card p-5" aria-labelledby="meta-h">
          <h2 id="meta-h" class="eyebrow">Metadados</h2>
          <dl class="mt-3 grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
            <dt class="text-muted">Status</dt>
            <dd data-testid="item-status">{{ statusLabel[item.status] ?? item.status }}</dd>
            <dt class="text-muted">Publicado</dt>
            <dd>{{ fmtDateTime(item.published_at) }}</dd>
            <dt class="text-muted">Coletado</dt>
            <dd>{{ fmtDateTime(item.fetched_at) }}</dd>
            <dt class="text-muted">Nota em</dt>
            <dd>{{ fmtDateTime(item.scored_at) }}</dd>
          </dl>
          <p v-if="item.last_error" class="mt-3 rounded-lg bg-danger/10 p-3 text-xs break-words text-danger" role="note">{{ item.last_error }}</p>
        </section>

        <section class="card p-5" aria-labelledby="cluster-h" data-testid="cluster">
          <div class="flex items-center gap-2">
            <AppIcon name="layers" :size="18" class="text-accent" />
            <h2 id="cluster-h" class="font-display font-semibold">Cluster</h2>
          </div>
          <template v-if="item.cluster">
            <p class="mt-2 text-sm text-white/85 break-words">{{ item.cluster.title }}</p>
            <p class="mt-1 font-mono text-xs text-muted">
              #{{ item.cluster.id }} · {{ item.cluster.item_count }} {{ item.cluster.item_count === 1 ? 'item' : 'itens' }} ·
              {{ item.is_primary ? 'este é o principal' : 'este é um duplicado' }}
            </p>
            <RouterLink
              v-if="!item.is_primary && item.cluster.representative_item_id"
              :to="`/items/${item.cluster.representative_item_id}`"
              class="btn-ghost mt-3 w-full"
              data-testid="open-primary"
              >Ver item principal</RouterLink
            >
          </template>
          <p v-else class="mt-2 text-sm text-muted">Ainda sem cluster — o dedup roda depois da nota.</p>

          <h3 class="mt-5 eyebrow">Duplicados agrupados ({{ duplicates.length }})</h3>
          <p v-if="duplicates.length === 0" class="mt-2 text-sm text-muted" data-testid="no-duplicates">Nenhuma outra fonte trouxe esta notícia.</p>
          <ul v-else class="mt-2 divide-y divide-line" data-testid="duplicates">
            <li v-for="d in duplicates" :key="d.id" class="py-3">
              <RouterLink :to="`/items/${d.id}`" class="text-sm font-medium break-words hover:text-primary" data-testid="open-duplicate">{{ d.title }}</RouterLink>
              <p class="mt-1 flex flex-wrap items-center gap-x-2 text-xs text-muted">
                <span>{{ d.source_name }}</span>
                <span>· nota <span class="font-mono text-primary">{{ fmtScore(d.score) }}</span></span>
                <span v-if="d.similarity !== null">· similaridade <span class="font-mono text-white">{{ (d.similarity * 100).toFixed(0) }}%</span></span>
                <a :href="d.url" target="_blank" rel="noopener noreferrer" class="ml-auto inline-flex size-9 items-center justify-center rounded-lg hover:bg-raised hover:text-white" :aria-label="`Abrir original: ${d.title} (nova aba)`" data-testid="duplicate-original">
                  <AppIcon name="external" :size="14" />
                </a>
              </p>
            </li>
          </ul>
        </section>
      </div>
    </div>
  </div>
</template>
