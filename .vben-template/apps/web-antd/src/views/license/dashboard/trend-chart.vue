<script lang="ts" setup>
import type { EchartsUIType } from '@vben/plugins/echarts';

import { ref, watch } from 'vue';

import { EchartsUI, useEcharts } from '@vben/plugins/echarts';

const props = defineProps<{
  daily: Array<{
    authorizations: number;
    date: string;
    logins: number;
  }>;
}>();

const chartRef = ref<EchartsUIType>();
const { renderEcharts } = useEcharts(chartRef);

function renderChart() {
  const days = props.daily || [];
  renderEcharts({
    color: ['#1677ff', '#52c41a'],
    grid: {
      bottom: 8,
      containLabel: true,
      left: 8,
      right: 16,
      top: 32,
    },
    legend: {
      data: ['登录', '新增授权'],
      right: 12,
      top: 0,
    },
    series: [
      {
        areaStyle: {
          opacity: 0.12,
        },
        data: days.map((item) => item.logins),
        name: '登录',
        smooth: true,
        symbolSize: 7,
        type: 'line',
      },
      {
        areaStyle: {
          opacity: 0.12,
        },
        data: days.map((item) => item.authorizations),
        name: '新增授权',
        smooth: true,
        symbolSize: 7,
        type: 'line',
      },
    ],
    tooltip: {
      trigger: 'axis',
    },
    xAxis: {
      axisTick: {
        show: false,
      },
      boundaryGap: false,
      data: days.map((item) => item.date.slice(5)),
      type: 'category',
    },
    yAxis: {
      minInterval: 1,
      splitLine: {
        lineStyle: {
          type: 'dashed',
        },
      },
      type: 'value',
    },
  });
}

watch(() => props.daily, renderChart, { deep: true, immediate: true });
</script>

<template>
  <EchartsUI ref="chartRef" class="h-80" />
</template>
