import { createRouter, createWebHashHistory } from 'vue-router'

const routes = [
  { path: '/', component: () => import('../views/Dashboard.vue') },
  { path: '/models', component: () => import('../views/Models.vue') },
  { path: '/usage', component: () => import('../views/Usage.vue') },
  { path: '/request-usage', component: () => import('../views/RequestUsage.vue') },
  { path: '/inspector', component: () => import('../views/Inspector.vue') },
  { path: '/:pathMatch(.*)*', redirect: '/' },
]

export const router = createRouter({
  history: createWebHashHistory(),
  routes,
})
