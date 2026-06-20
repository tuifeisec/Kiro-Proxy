<script lang="ts" setup>
import { computed, onMounted, ref } from 'vue';

import { AnalysisChartCard, AnalysisOverview } from '@vben/common-ui';
import { SvgBellIcon, SvgCardIcon, SvgDownloadIcon } from '@vben/icons';

import { Card, List, message } from 'ant-design-vue';

import { getDashboardStats, getState } from '#/api/license-admin';

import TrendChart from './trend-chart.vue';

const loading = ref(false);
const stats = ref({
  authorization_count: 0,
  recent_login_count: 0,
  recent_authorization_count: 0,
  daily: [],
});
const latestActivities = ref<string[]>([]);

const overviewItems = computed(() => [
  {
    icon: SvgCardIcon,
    title: '授权数量',
    totalTitle: '全部授权',
    totalValue: stats.value.authorization_count,
    value: stats.value.authorization_count,
  },
  {
    icon: SvgBellIcon,
    title: '近七日登录数量',
    totalTitle: '近七日登录',
    totalValue: stats.value.recent_login_count,
    value: stats.value.recent_login_count,
  },
  {
    icon: SvgDownloadIcon,
    title: '近七日添加数量',
    totalTitle: '近七日新增授权',
    totalValue: stats.value.recent_authorization_count,
    value: stats.value.recent_authorization_count,
  },
]);

async function loadData() {
  loading.value = true;
  try {
    const [dashboard, state] = await Promise.all([getDashboardStats(), getState()]);
    stats.value = dashboard as any;
    const auths = Object.values(state.authorizations || {})
      .sort((a: any, b: any) => String(b.created_at).localeCompare(String(a.created_at)))
      .slice(0, 6)
      .map((item: any) => `新增授权：${item.machine_code} ${item.remark ? `（${item.remark}）` : ''}`);
    latestActivities.value = auths.length ? auths : ['暂无授权动态'];
  } catch (error: any) {
    message.error(error.message || '加载仪表盘失败');
  } finally {
    loading.value = false;
  }
}

onMounted(loadData);
</script>

<template>
  <div class="p-5" v-loading="loading">
    <AnalysisOverview :items="overviewItems" />

    <div class="mt-5 grid grid-cols-1 gap-5 xl:grid-cols-[1fr_360px]">
      <AnalysisChartCard title="近七日趋势">
        <TrendChart :daily="stats.daily" />
      </AnalysisChartCard>

      <Card title="最新动态" :bordered="false">
        <List :data-source="latestActivities">
          <template #renderItem="{ item }">
            <List.Item>{{ item }}</List.Item>
          </template>
        </List>
      </Card>
    </div>
  </div>
</template>
