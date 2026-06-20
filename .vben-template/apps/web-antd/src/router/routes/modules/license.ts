import type { RouteRecordRaw } from 'vue-router';

const routes: RouteRecordRaw[] = [
    {
        meta: {
            icon: 'lucide:layout-dashboard',
            order: -10,
            title: '仪表盘',
        },
        name: 'Dashboard',
        path: '/dashboard',
        component: () => import('#/views/license/dashboard/index.vue'),
    },
    {
        meta: {
            icon: 'lucide:key-round',
            order: -9,
            title: '授权列表',
        },
        name: 'Authorizations',
        path: '/authorizations',
        component: () => import('#/views/license/authorizations/index.vue'),
    },
    {
        meta: {
            icon: 'lucide:megaphone',
            order: -8,
            title: '广告列表',
        },
        name: 'Advertisements',
        path: '/advertisements',
        component: () => import('#/views/license/advertisements/index.vue'),
    },
    {
        meta: {
            icon: 'lucide:rocket',
            order: -7,
            title: '版本更新',
        },
        name: 'Releases',
        path: '/releases',
        component: () => import('#/views/license/releases/index.vue'),
    },
];

export default routes;
