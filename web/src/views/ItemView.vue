<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { api, errorMessage } from '../api/client'
import type { ItemDetail } from '../api/types'
import AppIcon from '../components/AppIcon.vue'
import ScoreBadge from '../components/ScoreBadge.vue'
import StatePanel from '../components/StatePanel.vue'
import { fmtDateTime, fmtScore, hostOf, kindLabel } from '../lib/format'
import { staggerDelay } from '../lib/motion'

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

// Digest threshold decides the score colour, like in the feed.
const threshold = ref(6)
onMounted(async () => {
  try {
    threshold.value = (await api.settings()).score_threshold
  } catch {
    /* keep the default colour rule; the score itself comes from the item */
  }
})

const statusLabel: Record<string, string> = { raw: 'Aguardando nota', scored: 'Com nota', deduped: 'Processado', duplicate: 'Duplicado', failed: 'Falhou' }
const duplicates = computed(() => item.value?.duplicates ?? [])

function back() {
  if (window.history.state?.back) router.back()
  else router.push('/')
}
</script>

<template>
  <div class="pt-6 md:pt-10">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <button type="button" class="btn-ghost group" data-testid="back" @click="back">
        <AppIcon name="back" :size="16" /> Voltar
      </button>
      <p class="eyebrow"><span class="text-primary">01</span> — Feed / item <span class="num">#{{ route.params.id }}</span></p>
    </div>

    <div v-if="loading && !item" class="mt-10 space-y-4" data-testid="item-loading" aria-hidden="true">
      <div class="skeleton h-4 w-48" />
      <div class="skeleton h-14 w-4/5" />
      <div class="skeleton h-14 w-3/5" />
      <div class="mt-8 grid gap-8 lg:grid-cols-12">
        <div class="skeleton h-40 lg:col-span-8" />
        <div class="skeleton h-40 lg:col-span-4" />
      </div>
    </div>
    <StatePanel v-else-if="notFound" class="mt-8" kind="empty" title="Item não encontrado" detail="Ele pode ter sido removido junto com a fonte." testid="item-not-found">
      <RouterLink to="/" class="btn-primary" data-testid="go-feed">Ir para o feed</RouterLink>
    </StatePanel>
    <StatePanel v-else-if="error" class="mt-8" kind="error" title="Não foi possível carregar o item" :detail="error" @retry="load" />

    <article v-else-if="item" class="mt-8" data-testid="item-detail">
      <!-- Headline block -->
      <header class="grid gap-8 border-b border-line pb-10 lg:grid-cols-12">
        <div class="min-w-0 lg:col-span-9">
          <p class="flex flex-wrap items-center gap-2 font-mono text-xs text-muted">
            <span class="chip">{{ kindLabel[item.source_kind] ?? item.source_kind }}</span>
            <span v-if="item.source_name !== kindLabel[item.source_kind]" class="text-white/85">{{ item.source_name }}</span>
            <span v-if="item.author">/ por {{ item.author }}</span>
            <span>/ {{ hostOf(item.url) }}</span>
          </p>
          <h1 class="display-lg mt-4 break-words">{{ item.title }}</h1>
          <a :href="item.url" target="_blank" rel="noopener noreferrer" class="btn-primary group mt-7 w-full sm:w-auto" data-testid="item-original">
            Abrir original em {{ hostOf(item.url) }} <AppIcon name="external" :size="16" class="nudge" />
          </a>
        </div>
        <div class="flex items-end justify-between gap-6 border-line lg:col-span-3 lg:flex-col lg:items-start lg:justify-end lg:border-l lg:pl-8">
          <div>
            <p class="eyebrow mb-3">Nota do LLM</p>
            <ScoreBadge :score="item.score" :threshold="threshold" size="xl" delay="300ms" />
          </div>
          <p class="eyebrow text-right lg:text-left" data-testid="item-status">{{ statusLabel[item.status] ?? item.status }}</p>
        </div>
      </header>

      <div class="grid gap-10 pt-10 lg:grid-cols-12">
        <!-- Body -->
        <div class="min-w-0 lg:col-span-8">
          <section v-if="item.summary" v-reveal="0" aria-labelledby="sum-h">
            <h2 id="sum-h" class="eyebrow">Resumo</h2>
            <p class="mt-3 text-xl leading-relaxed text-white/90 md:text-2xl">{{ item.summary }}</p>
          </section>

          <section v-reveal="1" class="mt-10 border-l-2 border-primary py-1 pl-6" aria-labelledby="reason-h">
            <h2 id="reason-h" class="eyebrow text-primary">Por que essa nota</h2>
            <p class="mt-3 font-display text-lg leading-relaxed text-white/90 md:text-xl">{{ item.score_reason || 'O item ainda não recebeu nota do LLM.' }}</p>
          </section>

          <section v-if="item.tags?.length" v-reveal="2" class="mt-10" aria-labelledby="tags-h">
            <h2 id="tags-h" class="eyebrow">Tags</h2>
            <ul class="mt-3 flex flex-wrap gap-2">
              <li v-for="t in item.tags" :key="t">
                <RouterLink :to="{ path: '/', query: { tag: t, period: 'all' } }" class="chip min-h-11 px-4 hover:border-primary/60 hover:text-primary" data-testid="item-tag">#{{ t }}</RouterLink>
              </li>
            </ul>
          </section>

          <section v-if="item.content" v-reveal="3" class="mt-10" aria-labelledby="content-h">
            <h2 id="content-h" class="eyebrow">Conteúdo coletado</h2>
            <p class="mt-3 max-h-80 overflow-y-auto border-t border-line pt-4 pr-2 text-sm whitespace-pre-line text-white/65 break-words" tabindex="0">{{ item.content }}</p>
          </section>
        </div>

        <!-- Aside: meta + cluster -->
        <aside class="flex flex-col gap-10 lg:col-span-4">
          <section v-reveal="1" aria-labelledby="meta-h">
            <h2 id="meta-h" class="eyebrow">Metadados</h2>
            <dl class="mt-3 text-sm">
              <div v-for="[k, v] in ([['Publicado', item.published_at], ['Coletado', item.fetched_at], ['Nota em', item.scored_at]] as const)" :key="k" class="flex justify-between gap-4 border-t border-line py-3">
                <dt class="text-muted">{{ k }}</dt>
                <dd class="num">{{ fmtDateTime(v) }}</dd>
              </div>
            </dl>
            <p v-if="item.last_error" class="mt-3 rounded-md bg-danger/10 p-3 text-xs break-words text-danger" role="note">{{ item.last_error }}</p>
          </section>

          <section v-reveal="2" class="card p-5" aria-labelledby="cluster-h" data-testid="cluster">
            <div class="flex items-center justify-between gap-2">
              <h2 id="cluster-h" class="flex items-center gap-2 font-display text-lg font-semibold"><AppIcon name="layers" :size="18" class="text-accent" /> Cluster</h2>
              <span v-if="item.cluster" class="num text-xs text-muted">#{{ item.cluster.id }}</span>
            </div>
            <template v-if="item.cluster">
              <p class="mt-3 text-sm text-white/85 break-words">{{ item.cluster.title }}</p>
              <p class="mt-3 flex items-baseline gap-2">
                <span class="num text-4xl font-bold">{{ item.cluster.item_count }}</span>
                <span class="text-sm text-muted">{{ item.cluster.item_count === 1 ? 'item' : 'itens' }} · {{ item.is_primary ? 'este é o principal' : 'este é um duplicado' }}</span>
              </p>
              <RouterLink
                v-if="!item.is_primary && item.cluster.representative_item_id"
                :to="`/items/${item.cluster.representative_item_id}`"
                class="btn-ghost mt-3 w-full"
                data-testid="open-primary"
                >Ver item principal</RouterLink
              >
            </template>
            <p v-else class="mt-3 text-sm text-muted">Ainda sem cluster — o dedup roda depois da nota.</p>

            <h3 class="mt-6 eyebrow">Duplicados agrupados ({{ duplicates.length }})</h3>
            <p v-if="duplicates.length === 0" class="mt-2 text-sm text-muted" data-testid="no-duplicates">Nenhuma outra fonte trouxe esta notícia.</p>
            <ul v-else class="mt-2" data-testid="duplicates">
              <li v-for="(d, i) in duplicates" :key="d.id" class="border-t border-line py-3">
                <div class="flex items-start gap-2">
                  <RouterLink :to="`/items/${d.id}`" class="min-w-0 flex-1 py-1 text-sm font-medium break-words hover:text-primary" data-testid="open-duplicate">{{ d.title }}</RouterLink>
                  <a :href="d.url" target="_blank" rel="noopener noreferrer" class="-mt-1 inline-flex size-11 shrink-0 items-center justify-center rounded-md text-muted hover:bg-raised hover:text-white" :aria-label="`Abrir original: ${d.title} (nova aba)`" data-testid="duplicate-original">
                    <AppIcon name="external" :size="14" />
                  </a>
                </div>
                <p class="mt-1 flex flex-wrap items-center gap-x-2 font-mono text-xs text-muted">
                  <span>{{ d.source_name }}</span>
                  <span>· nota <span class="text-primary">{{ fmtScore(d.score) }}</span></span>
                </p>
                <div v-if="d.similarity !== null" class="mt-2 flex items-center gap-3">
                  <div class="h-1 flex-1 overflow-hidden rounded-full bg-white/10" aria-hidden="true">
                    <div class="meter-fill h-full bg-accent" :style="{ width: `${d.similarity * 100}%`, '--d': staggerDelay(i) }" />
                  </div>
                  <span class="num text-xs text-white">{{ (d.similarity * 100).toFixed(0) }}% similar</span>
                </div>
              </li>
            </ul>
          </section>
        </aside>
      </div>
    </article>
  </div>
</template>
