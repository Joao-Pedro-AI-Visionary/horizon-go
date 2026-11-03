<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, errorMessage } from '../api/client'
import type { Digest } from '../api/types'
import AppIcon from '../components/AppIcon.vue'
import StatePanel from '../components/StatePanel.vue'
import { fmtDateTime, fmtDay } from '../lib/format'
import { useToasts } from '../stores/toast'

const route = useRoute()
const router = useRouter()
const toasts = useToasts()

const digests = ref<Digest[]>([])
const loading = ref(true)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    digests.value = (await api.digests()).digests ?? []
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}
onMounted(load)

const selectedDate = computed(() => (route.params.date as string) || digests.value[0]?.date || '')
const selected = computed(() => digests.value.find((d) => d.date === selectedDate.value))
// Cache-busting key so the iframe reloads after a regeneration.
const frameKey = ref(0)
const frameLoading = ref(true)
watch([selectedDate, frameKey], () => (frameLoading.value = true))

function open(date: string) {
  router.push({ name: 'digests', params: { date } })
}

const generating = ref(false)
async function generate() {
  generating.value = true
  try {
    const d = await api.generateDigest()
    toasts.success(`Digest de ${fmtDay(d.date)} gerado com ${d.item_count} itens (de ${d.considered} considerados).`)
    await load()
    frameKey.value++
    open(d.date)
  } catch (err) {
    toasts.error(`Falha ao gerar o digest: ${errorMessage(err)}`)
  } finally {
    generating.value = false
  }
}
</script>

<template>
  <div class="mx-auto max-w-7xl">
    <header class="flex flex-wrap items-end justify-between gap-4">
      <div>
        <p class="eyebrow">Radar · digests</p>
        <h1 class="mt-1 font-display text-3xl font-bold md:text-4xl">Digest diário</h1>
        <p class="mt-1 text-sm text-muted">Os melhores itens de cada dia, acima da nota de corte, em HTML e Markdown.</p>
      </div>
      <button type="button" class="btn-primary" :disabled="generating" :aria-busy="generating" data-testid="generate-digest" @click="generate">
        <AppIcon :name="generating ? 'refresh' : 'plus'" :size="16" :class="generating ? 'animate-spin' : ''" />
        {{ generating ? 'Gerando…' : 'Gerar agora' }}
      </button>
    </header>

    <div v-if="loading && digests.length === 0" class="mt-5 grid gap-4 lg:grid-cols-[320px_1fr]" data-testid="digests-loading">
      <div class="card space-y-3 p-4">
        <div v-for="i in 4" :key="i" class="skeleton h-16" />
      </div>
      <div class="card skeleton h-96" />
    </div>
    <StatePanel v-else-if="error" class="mt-5" kind="error" title="Não foi possível carregar os digests" :detail="error" @retry="load" />
    <StatePanel
      v-else-if="digests.length === 0"
      class="mt-5"
      kind="empty"
      title="Nenhum digest ainda"
      detail="O cron do worker gera um por dia; use “Gerar agora” para criar o de hoje com os itens já pontuados."
      testid="digests-empty"
    />

    <div v-else class="mt-5 grid gap-4 lg:grid-cols-[320px_1fr]">
      <nav class="card h-fit p-2 lg:sticky lg:top-8" aria-label="Digests por data">
        <ul class="flex gap-2 overflow-x-auto p-1 lg:max-h-[calc(100dvh-12rem)] lg:flex-col lg:overflow-y-auto" data-testid="digest-list">
          <li v-for="d in digests" :key="d.id" class="shrink-0">
            <button
              type="button"
              class="w-full min-w-44 cursor-pointer rounded-xl px-4 py-3 text-left transition-colors duration-200"
              :class="d.date === selectedDate ? 'bg-primary text-ink' : 'hover:bg-raised'"
              :aria-current="d.date === selectedDate ? 'true' : undefined"
              data-testid="digest-item"
              @click="open(d.date)"
            >
              <span class="block font-display font-semibold capitalize">{{ fmtDay(d.date) }}</span>
              <span class="block font-mono text-xs" :class="d.date === selectedDate ? 'text-ink/75' : 'text-muted'">
                {{ d.item_count }} itens · {{ d.considered }} considerados
              </span>
            </button>
          </li>
        </ul>
      </nav>

      <section class="card min-w-0 overflow-hidden" aria-labelledby="digest-title" data-testid="digest-reader">
        <template v-if="selected">
          <div class="flex flex-wrap items-center justify-between gap-3 border-b border-line p-4">
            <div class="min-w-0">
              <h2 id="digest-title" class="font-display text-lg font-semibold break-words">{{ selected.title }}</h2>
              <p class="text-xs text-muted">Atualizado {{ fmtDateTime(selected.updated_at) }} · {{ selected.language }}</p>
            </div>
            <div class="flex gap-2">
              <a :href="api.digestHtmlUrl(selected.date)" target="_blank" rel="noopener" class="btn-ghost" data-testid="digest-open-html">
                <AppIcon name="external" :size="16" /> HTML
              </a>
              <a :href="api.digestMarkdownUrl(selected.date)" :download="`radar-${selected.date}.md`" class="btn-ghost" data-testid="digest-download-md">
                <AppIcon name="download" :size="16" /> Markdown
              </a>
            </div>
          </div>
          <div class="relative bg-ink">
            <div v-if="frameLoading" class="absolute inset-0 space-y-3 p-6" aria-hidden="true">
              <div class="skeleton h-8 w-1/2" />
              <div class="skeleton h-4 w-full" />
              <div class="skeleton h-4 w-4/5" />
            </div>
            <!-- Server HTML is rendered sandboxed (no scripts) under the API's CSP. -->
            <iframe
              :key="`${selected.date}-${frameKey}`"
              :src="`${api.digestHtmlUrl(selected.date)}&v=${frameKey}`"
              :title="`Digest ${selected.title}`"
              sandbox="allow-popups allow-popups-to-escape-sandbox"
              class="block h-[70dvh] w-full"
              data-testid="digest-frame"
              @load="frameLoading = false"
            />
          </div>
        </template>
        <StatePanel v-else kind="empty" title="Digest não encontrado" :detail="`Não há digest para ${selectedDate}.`" testid="digest-missing" />
      </section>
    </div>
  </div>
</template>
