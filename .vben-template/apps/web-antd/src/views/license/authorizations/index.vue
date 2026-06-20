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

import {
  authorizeMachine,
  deleteAuthorization,
  getState,
  saveBranding,
} from '#/api/license-admin';
import { formatDateTime } from '#/utils/date';

const loading = ref(false);
const drawerOpen = ref(false);
const keyword = ref('');
const items = ref<any[]>([]);
const branding = reactive({ app_name: 'Kiro Proxy', unauthorized_text: '请加入群获取免费授权后使用' });
const form = reactive({ machine_code: '', remark: '', enabled: true, traffic_logs: false });

const filteredItems = computed(() => {
  const q = keyword.value.trim().toLowerCase();
  if (!q) return items.value;
  return items.value.filter((item) =>
    [item.machine_code, item.remark].some((value) => String(value || '').toLowerCase().includes(q)),
  );
});

const columns = [
  { title: '机器码', dataIndex: 'machine_code', ellipsis: true },
  { title: '备注', dataIndex: 'remark' },
  { title: '状态', dataIndex: 'enabled', width: 100 },
  { title: '流量日志', dataIndex: 'traffic_logs', width: 110 },
  { title: '创建时间', dataIndex: 'created_at', width: 190 },
  { title: '上次监测时间', dataIndex: 'last_checked_at', width: 190 },
  { title: '操作', key: 'action', width: 100 },
];

async function loadData() {
  loading.value = true;
  try {
    const state = await getState();
    items.value = Object.values(state.authorizations || {}).sort((a: any, b: any) =>
      String(b.created_at).localeCompare(String(a.created_at)),
    );
    Object.assign(branding, state.branding || branding);
  } catch (error: any) {
    message.error(error.message || '加载授权列表失败');
  } finally {
    loading.value = false;
  }
}

function openCreateDrawer() {
  Object.assign(form, { machine_code: '', remark: '', enabled: true, traffic_logs: false });
  drawerOpen.value = true;
}

async function submitForm() {
  if (!form.machine_code.trim()) {
    message.warning('请输入机器码');
    return;
  }
  await authorizeMachine(form);
  message.success('授权已保存');
  drawerOpen.value = false;
  await loadData();
}

async function remove(machineCode: string) {
  await deleteAuthorization(machineCode);
  message.success('已删除');
  await loadData();
}

async function submitBranding() {
  await saveBranding(branding);
  message.success('未授权界面配置已保存');
}

onMounted(loadData);
</script>

<template>
  <div class="space-y-5 p-5">
    <Card title="未授权界面配置" :bordered="false">
      <div class="grid grid-cols-1 gap-3 xl:grid-cols-[260px_1fr_auto]">
        <Input v-model:value="branding.app_name" placeholder="应用名称" />
        <Input v-model:value="branding.unauthorized_text" placeholder="未授权提示文案" />
        <Button type="primary" @click="submitBranding">保存配置</Button>
      </div>
    </Card>

    <Card title="授权列表" :bordered="false">
      <template #extra>
        <Space>
          <Input.Search v-model:value="keyword" allow-clear placeholder="搜索机器码 / 备注" style="width: 260px" />
          <Button type="primary" @click="openCreateDrawer">添加授权</Button>
        </Space>
      </template>

      <Table :columns="columns" :data-source="filteredItems" :loading="loading" row-key="machine_code">
        <template #bodyCell="{ column, record }">
          <template v-if="column.dataIndex === 'enabled'">
            <Tag :color="record.enabled ? 'green' : 'red'">{{ record.enabled ? '启用' : '禁用' }}</Tag>
          </template>
          <template v-else-if="column.dataIndex === 'traffic_logs'">
            <Tag :color="record.traffic_logs ? 'blue' : 'default'">{{ record.traffic_logs ? '允许' : '禁止' }}</Tag>
          </template>
          <template v-else-if="column.dataIndex === 'created_at' || column.dataIndex === 'last_checked_at'">
            {{ formatDateTime(record[column.dataIndex]) }}
          </template>
          <template v-else-if="column.key === 'action'">
            <Popconfirm title="确认删除该授权？" @confirm="remove(record.machine_code)">
              <Button danger type="link">删除</Button>
            </Popconfirm>
          </template>
        </template>
      </Table>
    </Card>

    <Drawer v-model:open="drawerOpen" title="添加授权" width="520" destroy-on-close>
      <Form layout="vertical">
        <FormItem label="机器码" required>
          <Input v-model:value="form.machine_code" placeholder="请输入机器码" />
        </FormItem>
        <FormItem label="备注">
          <Input v-model:value="form.remark" placeholder="请输入备注" />
        </FormItem>
        <FormItem label="启用授权">
          <Switch v-model:checked="form.enabled" />
        </FormItem>
        <FormItem label="流量日志权限">
          <Switch v-model:checked="form.traffic_logs" />
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
