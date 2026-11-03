import { createRouter, createWebHistory } from 'vue-router'
import FeedView from '../views/FeedView.vue'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'feed', component: FeedView, meta: { title: 'Feed' } },
    { path: '/items/:id(\\d+)', name: 'item', component: () => import('../views/ItemView.vue'), meta: { title: 'Item' } },
    { path: '/digests/:date?', name: 'digests', component: () => import('../views/DigestsView.vue'), meta: { title: 'Digests' } },
    { path: '/sources', name: 'sources', component: () => import('../views/SourcesView.vue'), meta: { title: 'Fontes & Config' } },
    { path: '/metrics', name: 'metrics', component: () => import('../views/MetricsView.vue'), meta: { title: 'Métricas' } },
    { path: '/:pathMatch(.*)*', name: 'not-found', component: () => import('../views/NotFoundView.vue'), meta: { title: 'Não encontrado' } },
  ],
  scrollBehavior: (_to, _from, saved) => saved ?? { top: 0 },
})

router.afterEach((to) => {
  document.title = `${String(to.meta.title ?? 'Radar')} · Radar by JpDev`
})
