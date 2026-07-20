import { createRouter, createWebHashHistory } from 'vue-router'
import Dashboard from '../views/Dashboard.vue'
import Models from '../views/Models.vue'
import Inspector from '../views/Inspector.vue'
import Usage from '../views/Usage.vue'
import RequestUsage from '../views/RequestUsage.vue'

const routes = [
  { path: '/', component: Dashboard },
  { path: '/models', component: Models },
  { path: '/usage', component: Usage },
  { path: '/request-usage', component: RequestUsage },
  { path: '/inspector', component: Inspector },
]

export const router = createRouter({
  history: createWebHashHistory(),
  routes,
})
