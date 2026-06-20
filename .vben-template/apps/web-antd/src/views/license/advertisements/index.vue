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
  Space,
  Switch,
  Table,
  Tag,
} from 'ant-design-vue';

import { deleteAd, getState, saveAd } from '#/api/license-admin';
import { formatDateTime } from '#/utils/date';

const loading = ref(false);
const drawerOpen = ref(false);
const keyword = ref('');
const items = ref<any[]>([]);
const form = reactive({ id: '', title: '', link_url: '', image_url: '', description: '', enabled: true });

const filteredItems = computed(() => {
  const q = keyword.value.trim().toLowerCase();
  if (!q) return items.value;
  return items.value.filter((item) =>
    [item.title, item.description, item.link_url].some((value) => String(value || '').toLowerCase().includes(q)),
  );
});

const columns = [
  { title: '标题', dataIndex: 'title' },
  { title: '跳转地址', dataIndex: 'link_url', ellipsis: true },
  { title: '图片', dataIndex: 'image_url', ellipsis: true },
  { title: '描述', dataIndex: 'description', ellipsis: true },
  { title: '状态', dataIndex: 'enabled', width: 100 },
  { title: '创建时间', dataIndex: 'created_at', width: 190 },
  { title: '操作', key: 'action', width: 150 },
];

async function loadData() {
  loading.value = true;
  try {
    const state = await getState();
    items.value = state.ads || [];
  } catch (error: any) {
    message.error(error.message || '加载广告列表失败');
  } finally {
    loading.value = false;
  }
}

function openCreateDrawer() {
  Object.assign(form, { id: '', title: '', link_url: '', image_url: '', description: '', enabled: true });
  drawerOpen.value = true;
}

function openEditDrawer(record: any) {
  Object.assign(form, record);
  drawerOpen.value = true;
}

async function submitForm() {
  if (!form.title.trim()) {
    message.warning('请输入广告标题');
    return;
  }
  await saveAd(form);
  message.success('广告已保存');
  drawerOpen.value = false;
  await loadData();
}

async function remove(id: string) {
  await deleteAd(id);
  message.success('已删除');
  await loadData();
}

onMounted(loadData);
</script>

<template>
  <div class="p-5">
    <Card title="广告列表" :bordered="false">
      <template #extra>
        <Space>
          <Input.Search v-model:value="keyword" allow-clear placeholder="搜索标题 / 描述 / 链接" style="width: 280px" />
          <Button type="primary" @click="openCreateDrawer">添加广告</Button>
        </Space>
      </template>

      <Table :columns="columns" :data-source="filteredItems" :loading="loading" row-key="id">
        <template #bodyCell="{ column, record }">
          <template v-if="column.dataIndex === 'enabled'">
            <Tag :color="record.enabled ? 'green' : 'red'">{{ record.enabled ? '启用' : '禁用' }}</Tag>
          </template>
          <template v-else-if="column.dataIndex === 'created_at'">
            {{ formatDateTime(record.created_at) }}
          </template>
          <template v-else-if="column.key === 'action'">
            <Space>
              <Button type="link" @click="openEditDrawer(record)">编辑</Button>
              <Popconfirm title="确认删除该广告？" @confirm="remove(record.id)">
                <Button danger type="link">删除</Button>
              </Popconfirm>
            </Space>
          </template>
        </template>
      </Table>
    </Card>

    <Drawer v-model:open="drawerOpen" :title="form.id ? '编辑广告' : '添加广告'" width="560" destroy-on-close>
      <Form layout="vertical">
        <FormItem label="广告标题" required>
          <Input v-model:value="form.title" placeholder="请输入广告标题" />
        </FormItem>
        <FormItem label="点击跳转地址">
          <Input v-model:value="form.link_url" placeholder="请输入点击跳转地址" />
        </FormItem>
        <FormItem label="广告图片 URL" extra="建议图片尺寸：1200 × 300 px，系统会按固定广告位裁切展示。">
          <Input v-model:value="form.image_url" placeholder="请输入广告图片 URL" />
        </FormItem>
        <FormItem label="广告描述">
          <Input.TextArea v-model:value="form.description" :rows="4" placeholder="请输入广告描述" />
        </FormItem>
        <FormItem label="启用广告">
          <Switch v-model:checked="form.enabled" />
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
