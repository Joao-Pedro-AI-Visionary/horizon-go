import { createPinia } from 'pinia'
import { createApp } from 'vue'
import App from './App.vue'
import { vReveal } from './lib/motion'
import { router } from './router'
import './style.css'

createApp(App).use(createPinia()).use(router).directive('reveal', vReveal).mount('#app')
