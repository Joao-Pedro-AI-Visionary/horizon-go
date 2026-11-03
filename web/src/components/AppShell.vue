<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { api } from '../api/client'
import AppIcon from './AppIcon.vue'

// Top bar with numbered sections (md+) and a bottom tab bar (<768px).
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
const healthShort = { ok: 'Online', degraded: 'Degradada', down: 'Offline', loading: '…' }
const healthDot ={ ok: 'bg-ok', degraded: 'bg-accent', down: 'bg-danger', loading: 'bg-muted' }

function isActive(name: string, routeName: unknown) {
  const r = String(routeName ?? '')
  if (name === 'feed') return r === 'feed' || r === 'item'
  if (name === 'digests') return r.startsWith('digest')
  return r === name
}
</script>

<template>
  <div class="relative min-h-dvh">
    <div class="grid-rules" aria-hidden="true"><div><div /></div></div>
    <a
      href="#main"
      class="sr-only z-50 rounded-md bg-primary px-4 py-2 font-semibold text-ink focus:not-sr-only focus:fixed focus:top-3 focus:left-3"
      >Pular para o conteúdo</a
    >

    <header class="sticky top-0 z-40 border-b border-line bg-ink/85 backdrop-blur-md">
      <div class="container-ed flex h-16 items-stretch">
        <RouterLink to="/" class="group flex shrink-0 items-center gap-3 pr-5" data-testid="nav-logo" aria-label="Radar by JpDev — início">
          <img src="/jpdev-logo.png" alt="" class="h-9 w-auto" width="31" height="36" />
          <span class="leading-none">
            <span class="block font-display text-lg font-bold tracking-tight uppercase">Radar</span>
            <span class="mt-0.5 block font-mono text-[10px] tracking-[0.14em] text-muted uppercase">by JpDev</span>
          </span>
        </RouterLink>

        <nav class="hidden flex-1 items-stretch md:flex" aria-label="Navegação principal">
          <RouterLink
            v-for="(n, i) in nav"
            :key="n.to"
            :to="n.to"
            :data-testid="`nav-${n.name}`"
            :aria-current="isActive(n.name, $route.name) ? 'page' : undefined"
            class="group relative flex min-w-0 flex-1 items-center gap-2 border-l border-line px-4 font-display text-sm font-semibold tracking-tight uppercase transition-colors duration-200 lg:max-w-56"
            :class="isActive(n.name, $route.name) ? 'text-primary' : 'text-white/75 hover:text-white'"
          >
            <span class="font-mono text-[11px] font-normal text-muted">0{{ i + 1 }}</span>
            <span class="truncate"><span class="lg:hidden">{{ 'short' in n ? n.short : n.label }}</span><span class="hidden lg:inline">{{ n.label }}</span></span>
            <span
              class="absolute inset-x-0 -bottom-px h-0.5 origin-left bg-primary transition-transform duration-300 ease-[var(--ease-out-expo)]"
              :class="isActive(n.name, $route.name) ? 'scale-x-100' : 'scale-x-0 group-hover:scale-x-100 group-hover:bg-white/40'"
              aria-hidden="true"
            />
          </RouterLink>
        </nav>

        <div class="ml-auto flex items-center gap-2 border-line pl-4 font-mono text-[11px] tracking-[0.12em] text-muted uppercase md:border-l" role="status" data-testid="api-health">
          <span class="relative flex size-2.5">
            <span v-if="health === 'ok'" class="absolute inset-0 animate-ping rounded-full bg-ok/60 motion-reduce:hidden" aria-hidden="true" />
            <span class="relative size-2.5 rounded-full" :class="healthDot[health]" aria-hidden="true" />
          </span>
          <span class="hidden lg:inline">{{ healthText[health] }}</span>
          <span class="lg:hidden">{{ healthShort[health] }}</span>
        </div>
      </div>
    </header>

    <main id="main" tabindex="-1" class="relative z-10 container-ed pb-32 md:pb-16">
      <slot />
    </main>

    <!-- Bottom tab bar (mobile) -->
    <nav
      class="fixed inset-x-0 bottom-0 z-40 grid grid-cols-4 border-t border-line bg-ink/95 pb-[env(safe-area-inset-bottom)] backdrop-blur-md md:hidden"
      aria-label="Navegação principal"
    >
      <RouterLink
        v-for="n in nav"
        :key="n.to"
        :to="n.to"
        :data-testid="`bnav-${n.name}`"
        :aria-current="isActive(n.name, $route.name) ? 'page' : undefined"
        class="relative flex min-h-16 flex-col items-center justify-center gap-1 font-display text-[11px] font-semibold tracking-wide uppercase transition-colors duration-200"
        :class="isActive(n.name, $route.name) ? 'text-primary' : 'text-white/70'"
      >
        <span
          class="absolute inset-x-5 top-0 h-0.5 origin-center bg-primary transition-transform duration-300"
          :class="isActive(n.name, $route.name) ? 'scale-x-100' : 'scale-x-0'"
          aria-hidden="true"
        />
        <AppIcon :name="n.icon" :size="22" />
        {{ 'short' in n ? n.short : n.label }}
      </RouterLink>
    </nav>
  </div>
</template>
