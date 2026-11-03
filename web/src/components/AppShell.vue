<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { api } from '../api/client'
import AppIcon from './AppIcon.vue'

// Sidebar (>=1280px), icon rail (768–1279px), bottom nav (<768px).
const nav = [
  { to: '/', name: 'feed', label: 'Feed', icon: 'feed' },
  { to: '/digests', name: 'digests', label: 'Digests', icon: 'digest' },
  { to: '/sources', name: 'sources', label: 'Fontes & Config', short: 'Fontes', icon: 'sources' },
  { to: '/metrics', name: 'metrics', label: 'Métricas', icon: 'metrics' },
] as const

// Real API health (GET /healthz), refreshed every 30s.
const health = ref<'ok' | 'degraded' | 'down' | 'loading'>('loading')
let timer: ReturnType<typeof setInterval> | undefined
async function check() {
  try {
    const h = await api.health()
    health.value = h.status === 'ok' ? 'ok' : 'degraded'
  } catch (err) {
    health.value = (err as { status?: number }).status === 503 ? 'degraded' : 'down'
  }
}
onMounted(() => {
  check()
  timer = setInterval(check, 30_000)
})
onUnmounted(() => clearInterval(timer))

const healthText = { ok: 'API online', degraded: 'API degradada', down: 'API offline', loading: 'Verificando API' }
const healthDot = { ok: 'bg-ok', degraded: 'bg-accent', down: 'bg-danger', loading: 'bg-muted' }

function isActive(name: string, routeName: unknown) {
  const r = String(routeName ?? '')
  if (name === 'feed') return r === 'feed' || r === 'item'
  if (name === 'digests') return r.startsWith('digest')
  return r === name
}
</script>

<template>
  <div class="min-h-dvh md:flex">
    <a
      href="#main"
      class="sr-only z-50 rounded-lg bg-primary px-4 py-2 font-semibold text-ink focus:not-sr-only focus:fixed focus:top-3 focus:left-3"
      >Pular para o conteúdo</a
    >

    <!-- Sidebar / rail -->
    <div class="hidden shrink-0 border-r border-line bg-surface md:block md:w-20 xl:w-64">
    <aside class="sticky top-0 flex h-dvh flex-col" aria-label="Navegação principal">
      <RouterLink to="/" class="flex items-center gap-3 px-4 py-5 xl:px-6" data-testid="nav-logo" aria-label="Radar by JpDev — início">
        <img src="/jpdev-logo.png" alt="" class="h-10 w-auto shrink-0" width="34" height="40" />
        <span class="hidden leading-tight xl:block">
          <span class="block font-display text-lg font-bold">Radar</span>
          <span class="block text-xs text-muted">by JpDev</span>
        </span>
      </RouterLink>
      <nav class="flex flex-1 flex-col gap-1 px-3 xl:px-4">
        <RouterLink
          v-for="n in nav"
          :key="n.to"
          :to="n.to"
          :data-testid="`nav-${n.name}`"
          :title="n.label"
          :aria-current="isActive(n.name, $route.name) ? 'page' : undefined"
          class="flex min-h-12 items-center justify-center gap-3 rounded-xl px-3 text-sm font-medium transition-colors duration-200 xl:justify-start"
          :class="isActive(n.name, $route.name) ? 'bg-primary text-ink' : 'text-white/80 hover:bg-raised hover:text-white'"
        >
          <AppIcon :name="n.icon" />
          <span class="sr-only xl:not-sr-only">{{ n.label }}</span>
        </RouterLink>
      </nav>
      <div class="flex items-center justify-center gap-2 px-4 py-5 text-xs text-muted xl:justify-start xl:px-6" role="status" data-testid="api-health">
        <span class="size-2.5 shrink-0 rounded-full" :class="healthDot[health]" aria-hidden="true" />
        <span class="sr-only xl:not-sr-only">{{ healthText[health] }}</span>
      </div>
    </aside>
    </div>

    <!-- Mobile top bar -->
    <header class="sticky top-0 z-30 flex items-center justify-between border-b border-line bg-ink/90 px-4 py-3 backdrop-blur md:hidden">
      <RouterLink to="/" class="flex items-center gap-2" aria-label="Radar by JpDev — início" data-testid="nav-logo-mobile">
        <img src="/jpdev-logo.png" alt="" class="h-8 w-auto" width="27" height="32" />
        <span class="font-display font-bold">Radar <span class="font-normal text-muted">by JpDev</span></span>
      </RouterLink>
      <span class="flex items-center gap-2 text-xs text-muted" role="status">
        <span class="size-2.5 rounded-full" :class="healthDot[health]" aria-hidden="true" />{{ healthText[health] }}
      </span>
    </header>

    <main id="main" tabindex="-1" class="min-w-0 flex-1 px-4 pt-5 pb-28 md:px-8 md:pt-8 md:pb-10">
      <slot />
    </main>

    <!-- Bottom nav -->
    <nav
      class="fixed inset-x-0 bottom-0 z-30 grid grid-cols-4 border-t border-line bg-surface/95 pb-[env(safe-area-inset-bottom)] backdrop-blur md:hidden"
      aria-label="Navegação principal"
    >
      <RouterLink
        v-for="n in nav"
        :key="n.to"
        :to="n.to"
        :data-testid="`bnav-${n.name}`"
        :aria-current="isActive(n.name, $route.name) ? 'page' : undefined"
        class="flex min-h-16 flex-col items-center justify-center gap-1 text-[11px] font-medium"
        :class="isActive(n.name, $route.name) ? 'text-primary' : 'text-white/75'"
      >
        <AppIcon :name="n.icon" :size="22" />
        {{ 'short' in n ? n.short : n.label }}
      </RouterLink>
    </nav>
  </div>
</template>
