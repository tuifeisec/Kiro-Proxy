import { createRouter, createWebHashHistory } from 'vue-router'
import Dashboard from '../views/Dashboard.vue'
import Models from '../views/Models.vue'
import Inspector from '../views/Inspector.vue'

const routes = [
  { path: '/', component: Dashboard },
  { path: '/models', component: Models },
  { path: '/inspector', component: Inspector },
]

export const router = createRouter({
  history: createWebHashHistory(),
  routes,
})
