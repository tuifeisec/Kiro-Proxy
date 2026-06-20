<script setup>
import { computed, onMounted, ref } from 'vue'
import { addRelease, authorizeMachine, deleteAd, deleteRelease, getState, login, logout, saveAd, saveBranding } from './api/admin.js'

const loggedIn = ref(false)
const loading = ref(false)
const errorMessage = ref('')
const loginForm = ref({ username: 'superadmin', password: '' })
const state = ref({ authorizations: {}, releases: [], ads: [] })
const authForm = ref({ machine_code: '', remark: '', enabled: true, traffic_logs: false })
const releaseForm = ref({ version: '', notes: '', download_url: '', expected_sha256: '' })
const adForm = ref({ title: '', link_url: '', image_url: '', description: '', enabled: true })
const brandingForm = ref({ app_name: 'Kiro Proxy', unauthorized_text: '请加入群获取免费授权后使用' })

const authorizations = computed(() => Object.values(state.value.authorizations || {}))

async function loadState() {
  state.value = await getState()
  brandingForm.value = state.value.branding || { app_name: 'Kiro Proxy', unauthorized_text: '请加入群获取免费授权后使用' }
  loggedIn.value = true
}

async function handleLogin() {
  loading.value = true
  errorMessage.value = ''
  try {
    await login(loginForm.value)
    await loadState()
  } catch (error) {
    errorMessage.value = error.message
  } finally {
    loading.value = false
  }
}

async function handleLogout() {
  await logout()
  loggedIn.value = false
}

async function submitBranding() {
  errorMessage.value = ''
  try {
    await saveBranding(brandingForm.value)
    await loadState()
  } catch (error) {
    errorMessage.value = error.message
  }
}

async function submitAuthorization() {
  errorMessage.value = ''
  try {
    await authorizeMachine(authForm.value)
    authForm.value = { machine_code: '', remark: '', enabled: true, traffic_logs: false }
    await loadState()
  } catch (error) {
    errorMessage.value = error.message
  }
}

async function submitRelease() {
  errorMessage.value = ''
  try {
    await addRelease(releaseForm.value)
    releaseForm.value = { version: '', notes: '', download_url: '', expected_sha256: '' }
    await loadState()
  } catch (error) {
    errorMessage.value = error.message
  }
}

async function removeRelease(version) {
  if (!window.confirm(`删除版本 ${version}？`)) return
  await deleteRelease(version)
  await loadState()
}

async function submitAd() {
  errorMessage.value = ''
  try {
    await saveAd(adForm.value)
    adForm.value = { title: '', link_url: '', image_url: '', description: '', enabled: true }
    await loadState()
  } catch (error) {
    errorMessage.value = error.message
  }
}

async function removeAd(id) {
  if (!window.confirm('删除该广告？')) return
  await deleteAd(id)
  await loadState()
}

onMounted(async () => {
  try {
    await loadState()
  } catch {
    loggedIn.value = false
  }
})
</script>

<template>
  <main v-if="!loggedIn" class="login-page">
    <section class="card login-card">
      <h1>Kiro 授权后台</h1>
      <p class="muted">管理员登录</p>
      <input v-model="loginForm.username" placeholder="账号" />
      <input v-model="loginForm.password" placeholder="密码" type="password" @keyup.enter="handleLogin" />
      <button :disabled="loading" @click="handleLogin">{{ loading ? '登录中...' : '登录' }}</button>
      <p v-if="errorMessage" class="error">{{ errorMessage }}</p>
    </section>
  </main>

  <main v-else class="page">
    <header class="page-header">
      <div>
        <h1>授权后台</h1>
        <p class="muted">机器码授权 / 版本更新 / 广告系统</p>
      </div>
      <button class="secondary" @click="handleLogout">退出</button>
    </header>

    <p v-if="errorMessage" class="error">{{ errorMessage }}</p>

    <section class="card">
      <h2>未授权界面配置</h2>
      <div class="form-row">
        <input v-model="brandingForm.app_name" placeholder="应用名称" />
        <input v-model="brandingForm.unauthorized_text" class="wide" placeholder="未授权提示文案" />
        <button @click="submitBranding">保存配置</button>
      </div>
    </section>

    <section class="card">
      <h2>快速授权机器码</h2>
      <div class="form-row">
        <input v-model="authForm.machine_code" class="wide" placeholder="机器码" />
        <input v-model="authForm.remark" placeholder="备注" />
        <label class="check"><input v-model="authForm.traffic_logs" type="checkbox" /> 流量日志权限</label>
        <button @click="submitAuthorization">授权</button>
      </div>
    </section>

    <div class="grid">
      <section class="card">
        <h2>版本更新</h2>
        <div class="form-stack">
          <input v-model="releaseForm.version" placeholder="版本号" />
          <input v-model="releaseForm.download_url" placeholder="下载地址" />
          <input v-model="releaseForm.expected_sha256" placeholder="期望 SHA256（可选）" />
          <textarea v-model="releaseForm.notes" placeholder="更新记录"></textarea>
          <button @click="submitRelease">添加版本</button>
        </div>
      </section>

      <section class="card">
        <h2>广告系统</h2>
        <div class="form-stack">
          <input v-model="adForm.title" placeholder="广告标题" />
          <input v-model="adForm.link_url" placeholder="点击跳转地址" />
          <input v-model="adForm.image_url" placeholder="广告图片 URL" />
          <textarea v-model="adForm.description" placeholder="广告描述"></textarea>
          <label class="check"><input v-model="adForm.enabled" type="checkbox" /> 启用</label>
          <button @click="submitAd">保存广告</button>
        </div>
      </section>
    </div>

    <section class="card">
      <h2>已授权机器</h2>
      <table>
        <thead><tr><th>机器码</th><th>备注</th><th>状态</th><th>流量日志</th><th>时间</th></tr></thead>
        <tbody>
          <tr v-for="item in authorizations" :key="item.machine_code">
            <td class="mono">{{ item.machine_code }}</td>
            <td>{{ item.remark || '-' }}</td>
            <td><span class="badge" :class="{ disabled: !item.enabled }">{{ item.enabled ? '启用' : '禁用' }}</span></td>
            <td><span class="badge" :class="{ disabled: !item.traffic_logs }">{{ item.traffic_logs ? '允许' : '禁止' }}</span></td>
            <td>{{ item.created_at }}</td>
          </tr>
        </tbody>
      </table>
    </section>

    <section class="card">
      <h2>版本列表</h2>
      <table>
        <thead><tr><th>版本</th><th>下载地址</th><th>SHA256</th><th>更新记录</th><th>操作</th></tr></thead>
        <tbody>
          <tr v-for="item in state.releases" :key="item.version">
            <td>{{ item.version }}</td>
            <td class="mono">{{ item.download_url }}</td>
            <td class="mono">{{ item.expected_sha256 || '-' }}</td>
            <td>{{ item.notes || '-' }}</td>
            <td><button class="danger" @click="removeRelease(item.version)">删除</button></td>
          </tr>
        </tbody>
      </table>
    </section>

    <section class="card">
      <h2>广告列表</h2>
      <table>
        <thead><tr><th>标题</th><th>跳转地址</th><th>图片</th><th>描述</th><th>状态</th><th>操作</th></tr></thead>
        <tbody>
          <tr v-for="item in state.ads" :key="item.id">
            <td>{{ item.title }}</td>
            <td class="mono">{{ item.link_url || '-' }}</td>
            <td class="mono">{{ item.image_url || '-' }}</td>
            <td>{{ item.description || '-' }}</td>
            <td>{{ item.enabled ? '启用' : '禁用' }}</td>
            <td><button class="danger" @click="removeAd(item.id)">删除</button></td>
          </tr>
        </tbody>
      </table>
    </section>
  </main>
</template>
