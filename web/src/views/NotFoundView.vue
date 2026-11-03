<script setup lang="ts">
import { RouterLink } from 'vue-router'
import StatePanel from '../components/StatePanel.vue'
</script>

<template>
  <StatePanel kind="empty" title="Página não encontrada" detail="O endereço não corresponde a nenhuma tela do Radar.">
    <RouterLink to="/" class="btn-primary" data-testid="go-feed">Ir para o feed</RouterLink>
  </StatePanel>
</template>
