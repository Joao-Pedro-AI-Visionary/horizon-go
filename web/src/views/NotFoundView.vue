<script setup lang="ts">
import { RouterLink, useRoute } from 'vue-router'
import AppIcon from '../components/AppIcon.vue'

const route = useRoute()
</script>

<template>
  <section class="flex min-h-[70dvh] flex-col justify-center border-b border-line py-12" aria-labelledby="nf-h">
    <p class="eyebrow"><span class="text-primary">404</span> — rota desconhecida</p>
    <p class="num mt-6 text-[clamp(7rem,26vw,20rem)] leading-[0.8] font-bold text-primary" aria-hidden="true">404</p>
    <h1 id="nf-h" class="display-lg mt-6 max-w-3xl">Página não encontrada</h1>
    <p class="mt-3 max-w-xl text-white/70">
      O endereço <span class="font-mono break-all text-white">{{ route.fullPath }}</span> não corresponde a nenhuma tela do Radar.
    </p>
    <div class="mt-8">
      <RouterLink to="/" class="btn-primary group" data-testid="go-feed">Ir para o feed <AppIcon name="chevronRight" :size="16" class="nudge" /></RouterLink>
    </div>
  </section>
</template>
