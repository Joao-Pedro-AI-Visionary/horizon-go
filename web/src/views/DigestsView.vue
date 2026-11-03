<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, errorMessage } from '../api/client'
import type { Digest } from '../api/types'
import AppIcon from '../components/AppIcon.vue'
import PageHeader from '../components/PageHeader.vue'
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
  <div>
    <PageHeader index="02" section="Digests" title="Digest diário" :muted="1" lead="Os melhores itens de cada dia, acima da nota de corte, em HTML e Markdown.">
      <template #meta><span class="num">{{ digests.length }}</span> {{ digests.length === 1 ? 'edição' : 'edições' }}</template>
      <template #actions>
        <button type="button" class="btn-primary" :disabled="generating" :aria-busy="generating" data-testid="generate-digest" @click="generate">
          <AppIcon :name="generating ? 'refresh' : 'plus'" :size="16" :class="generating ? 'animate-spin' : ''" />
          {{ generating ? 'Gerando…' : 'Gerar agora' }}
        </button>
      </template>
    </PageHeader>

    <div v-if="loading && digests.length === 0" class="mt-8 grid gap-8 lg:grid-cols-12" data-testid="digests-loading" aria-hidden="true">
      <div class="space-y-3 lg:col-span-4">
        <div v-for="i in 4" :key="i" class="skeleton h-20" />
      </div>
      <div class="skeleton h-96 lg:col-span-8" />
    </div>
    <StatePanel v-else-if="error" class="mt-8" kind="error" title="Não foi possível carregar os digests" :detail="error" @retry="load" />
    <StatePanel
      v-else-if="digests.length === 0"
      class="mt-8"
      kind="empty"
      title="Nenhum digest ainda"
      detail="O cron do worker gera um por dia; use “Gerar agora” para criar o de hoje com os itens já pontuados."
      testid="digests-empty"
    />

    <div v-else class="mt-8 grid gap-8 lg:grid-cols-12">
      <!-- Issue index -->
      <nav class="min-w-0 lg:sticky lg:top-24 lg:col-span-4 lg:h-fit" aria-label="Digests por data">
        <p class="eyebrow mb-3">Edições</p>
        <ul class="-mx-1 flex gap-2 overflow-x-auto px-1 pb-2 lg:mx-0 lg:block lg:max-h-[calc(100dvh-12rem)] lg:overflow-y-auto lg:px-0 lg:pb-0" data-testid="digest-list">
          <li v-for="(d, i) in digests" :key="d.id" class="shrink-0 lg:border-t lg:border-line" v-reveal="i">
            <button
              type="button"
              class="group relative w-full min-w-52 cursor-pointer rounded-md border border-line px-4 py-3 text-left transition-colors duration-200 lg:rounded-none lg:border-0 lg:py-4 lg:pl-5"
              :class="d.date === selectedDate ? 'bg-surface text-white' : 'text-white/80 hover:bg-white/[0.03] hover:text-white'"
              :aria-current="d.date === selectedDate ? 'true' : undefined"
              data-testid="digest-item"
              @click="open(d.date)"
            >
              <span
                class="absolute top-0 bottom-0 left-0 w-0.5 origin-top bg-primary transition-transform duration-300 max-lg:inset-x-0 max-lg:top-auto max-lg:h-0.5 max-lg:w-auto max-lg:origin-left"
                :class="d.date === selectedDate ? 'scale-100' : 'max-lg:scale-x-0 lg:scale-y-0'"
                aria-hidden="true"
              />
              <span class="num block text-xs text-muted">Nº {{ String(digests.length - i).padStart(2, '0') }}</span>
              <span class="mt-0.5 block font-display text-lg font-semibold tracking-tight" :class="d.date === selectedDate ? 'text-primary' : ''">{{ fmtDay(d.date) }}</span>
              <span class="num block text-xs text-muted">{{ d.item_count }} itens · {{ d.considered }} considerados</span>
            </button>
          </li>
        </ul>
      </nav>

      <!-- Reader -->
      <section class="min-w-0 lg:col-span-8" aria-labelledby="digest-title" data-testid="digest-reader">
        <template v-if="selected">
          <div class="flex flex-wrap items-end justify-between gap-4 border-b border-line pb-4">
            <div class="min-w-0">
              <p class="eyebrow">Edição de {{ fmtDay(selected.date) }}</p>
              <h2 id="digest-title" class="mt-1 font-display text-2xl font-bold tracking-tight break-words">{{ selected.title }}</h2>
              <p class="num mt-1 text-xs text-muted">atualizado {{ fmtDateTime(selected.updated_at) }} · {{ selected.language }}</p>
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
          <dl class="grid grid-cols-2 border-b border-line">
            <div class="py-4 pr-3">
              <dt class="eyebrow">Itens no digest</dt>
              <dd class="num mt-1 text-3xl font-bold text-primary">{{ selected.item_count }}</dd>
            </div>
            <div class="border-l border-line py-4 pl-4">
              <dt class="eyebrow">Considerados</dt>
              <dd class="num mt-1 text-3xl font-bold">{{ selected.considered }}</dd>
            </div>
          </dl>
          <div class="relative mt-6 overflow-hidden rounded-[10px] border border-line bg-ink">
            <Transition name="drop">
              <div v-if="frameLoading" class="absolute inset-0 z-10 space-y-3 bg-ink p-6" aria-hidden="true">
                <div class="skeleton h-8 w-1/2" />
                <div class="skeleton h-4 w-full" />
                <div class="skeleton h-4 w-4/5" />
              </div>
            </Transition>
            <!-- Server HTML is rendered sandboxed (no scripts) under the API's CSP. -->
            <iframe
              :key="`${selected.date}-${frameKey}`"
              :src="`${api.digestHtmlUrl(selected.date)}&v=${frameKey}`"
              :title="`Digest ${selected.title}`"
              sandbox="allow-popups allow-popups-to-escape-sandbox"
              class="block h-[72dvh] w-full"
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
