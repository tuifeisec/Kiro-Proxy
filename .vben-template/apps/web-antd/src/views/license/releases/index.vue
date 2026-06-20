<script lang="ts" setup>
import { computed, onMounted, reactive, ref } from 'vue';

import {
  Button,
  Card,
  Drawer,
  Form,
  FormItem,
  Input,
  message,
  Popconfirm,
  Select,
  Space,
  Table,
  Tag,
} from 'ant-design-vue';

import { addRelease, deleteRelease, getState } from '#/api/license-admin';
import { formatDateTime } from '#/utils/date';

const loading = ref(false);
const drawerOpen = ref(false);
const keyword = ref('');
const items = ref<any[]>([]);
const form = reactive({ platform: 'darwin', version: '', notes: '', download_url: '', expected_sha256: '' });
const platformOptions = [
  { label: 'macOS', value: 'darwin' },
  { label: 'Windows', value: 'windows' },
];

const filteredItems = computed(() => {
  const q = keyword.value.trim().toLowerCase();
  if (!q) return items.value;
  return items.value.filter((item) =>
    [item.platform, item.version, item.notes, item.download_url, item.expected_sha256].some((value) => String(value || '').toLowerCase().includes(q)),
  );
});

const columns = [
  { title: '系统', dataIndex: 'platform', width: 110 },
  { title: '版本', dataIndex: 'version', width: 140 },
  { title: '下载地址', dataIndex: 'download_url', ellipsis: true },
  { title: '期望 SHA256', dataIndex: 'expected_sha256', ellipsis: true },
  { title: '更新记录', dataIndex: 'notes', ellipsis: true },
  { title: '创建时间', dataIndex: 'created_at', width: 190 },
  { title: '操作', key: 'action', width: 100 },
];

async function loadData() {
  loading.value = true;
  try {
    const state = await getState();
    items.value = state.releases || [];
  } catch (error: any) {
    message.error(error.message || '加载版本列表失败');
  } finally {
    loading.value = false;
  }
}

function openCreateDrawer() {
  Object.assign(form, { platform: 'darwin', version: '', notes: '', download_url: '', expected_sha256: '' });
  drawerOpen.value = true;
}

async function submitForm() {
  if (!form.version.trim() || !form.download_url.trim() || !form.expected_sha256.trim()) {
    message.warning('请输入版本号、下载地址和期望 SHA256');
    return;
  }
  await addRelease(form);
  message.success('版本已添加');
  drawerOpen.value = false;
  await loadData();
}

async function remove(record: any) {
  await deleteRelease(record.platform, record.version);
  message.success('已删除');
  await loadData();
}

onMounted(loadData);
</script>

<template>
  <div class="p-5">
    <Card title="版本更新" :bordered="false">
      <template #extra>
        <Space>
          <Input.Search v-model:value="keyword" allow-clear placeholder="搜索版本 / 记录 / 下载地址" style="width: 300px" />
          <Button type="primary" @click="openCreateDrawer">添加版本</Button>
        </Space>
      </template>

      <Table :columns="columns" :data-source="filteredItems" :loading="loading" :row-key="(record) => `${record.platform}:${record.version}`">
        <template #bodyCell="{ column, record }">
          <template v-if="column.dataIndex === 'platform'">
            <Tag :color="record.platform === 'darwin' ? 'blue' : 'green'">
              {{ record.platform === 'darwin' ? 'macOS' : 'Windows' }}
            </Tag>
          </template>
          <template v-else-if="column.dataIndex === 'created_at'">
            {{ formatDateTime(record.created_at) }}
          </template>
          <template v-else-if="column.key === 'action'">
            <Popconfirm title="确认删除该版本？" @confirm="remove(record)">
              <Button danger type="link">删除</Button>
            </Popconfirm>
          </template>
        </template>
      </Table>
    </Card>

    <Drawer v-model:open="drawerOpen" title="添加版本" width="560" destroy-on-close>
      <Form layout="vertical">
        <FormItem label="系统" required>
          <Select v-model:value="form.platform" :options="platformOptions" />
        </FormItem>
        <FormItem label="版本号" required>
          <Input v-model:value="form.version" placeholder="例如 v1.0.0" />
        </FormItem>
        <FormItem label="下载地址" required>
          <Input v-model:value="form.download_url" placeholder="请输入下载地址" />
        </FormItem>
        <FormItem label="期望 SHA256" required>
          <Input v-model:value="form.expected_sha256" placeholder="请输入该版本安装包的 SHA256" />
        </FormItem>
        <FormItem label="更新记录">
          <Input.TextArea v-model:value="form.notes" :rows="5" placeholder="请输入更新记录" />
        </FormItem>
      </Form>
      <template #footer>
        <Space>
          <Button @click="drawerOpen = false">取消</Button>
          <Button type="primary" @click="submitForm">保存</Button>
        </Space>
      </template>
    </Drawer>
  </div>
</template>
