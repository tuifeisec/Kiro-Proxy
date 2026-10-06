// 本地构建产物 + HTTP Mock；不调用真实代理、收费接口或截图 API。
// npm.cmd --prefix frontend run build 后：
// $env:PLAYWRIGHT_MODULE='<已安装的 playwright 包路径>'; node frontend/scripts/verify-settings-ui.mjs
import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { createServer } from 'node:http'
import { readFile } from 'node:fs/promises'
import { dirname, extname, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright')
const dist = resolve(dirname(fileURLToPath(import.meta.url)), '../dist')
const contentTypes = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml' }
const server = createServer(async (request, response) => {
  const pathname = decodeURIComponent(new URL(request.url, 'http://localhost').pathname)
  const path = resolve(dist, pathname === '/' ? 'index.html' : `.${pathname}`)
  if (!path.startsWith(`${dist}${sep}`)) {
    response.writeHead(403).end()
    return
  }
  try {
    response.writeHead(200, { 'Content-Type': contentTypes[extname(path)] || 'application/octet-stream' })
    response.end(await readFile(path))
  } catch {
    response.writeHead(404).end()
  }
})
await new Promise((done) => server.listen(0, '127.0.0.1', done))
const origin = `http://127.0.0.1:${server.address().port}`
let browser
const results = []

const upstreamModels = [
  { id: 'm-default', name: 'Light', model_id: 'mock-light', provider: 'openai', enabled: true, image_input_mode: 'unsupported', input_price: 1, output_price: 2 },
  { id: 'm-vision', name: 'Vision', model_id: 'mock-vision', provider: 'anthropic', enabled: true, image_input_mode: 'supported', input_price: 2, output_price: 4 },
]
const autoDefaults = () => ({
  enabled: false, default_model_id: '', vision_model_id: '', reasoning_model_id: '',
  long_context_model_id: '', fast_model_id: '', fast_max_chars: 500, long_context_min_chars: 32000, task_rules: [],
})
const typesafeDefaults = () => ({
  enabled: false, api_key: '****', model: 'jev-1.13.0', base_url: 'https://api.typesafe.ai',
  timeout_seconds: 10, input_price_per_million: 0.042, proxy_url: '',
})

async function fixture({ models = upstreamModels, settings = {}, failModels = false } = {}) {
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, reducedMotion: 'reduce' })
  context.setDefaultTimeout(7000)
  const state = {
    models: structuredClone(models),
    settings: { classifier_model_id: '', auto_routing_config: JSON.stringify(autoDefaults()), typesafe_classifier_config: JSON.stringify(typesafeDefaults()), ...settings },
    writes: [], requests: [], errors: [], failGet: 0, failPut: false, failReadAfterPut: false, holdPut: null, failModels,
  }
  await context.route('**/*', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    if (url.origin !== origin) {
      state.errors.push(`阻止外部请求：${url.origin}`)
      await route.abort()
      return
    }
    if (!url.pathname.startsWith('/api/')) return route.continue()
    state.requests.push([request.method(), url.pathname])
    let data = {}
    let status = 200
    if (url.pathname === '/api/settings') {
      if (request.method() === 'PUT') {
        const payload = request.postDataJSON()
        state.writes.push(payload)
        if (state.holdPut) await state.holdPut
        const submittedConfig = payload.typesafe_classifier_config ? JSON.parse(payload.typesafe_classifier_config) : null
        if (state.failPut) {
          status = 400
          data = { error: 'Mock 保存失败' }
        } else if (submittedConfig?.enabled && !submittedConfig.api_key) {
          status = 400
          data = { error: 'api_key is required when Typesafe classifier is enabled' }
        } else {
          Object.assign(state.settings, payload)
          if (payload.typesafe_classifier_config) {
            const config = JSON.parse(payload.typesafe_classifier_config)
            config.api_key = config.api_key ? '****' : ''
            state.settings.typesafe_classifier_config = JSON.stringify(config)
          }
          if (state.failReadAfterPut) state.failGet++
        }
      } else if (state.failGet > 0) {
        state.failGet--
        status = 503
        data = { error: 'Mock 读取失败' }
      } else data = state.settings
    } else if (url.pathname === '/api/models') {
      if (state.failModels) {
        status = 503
        data = { error: 'Mock 模型列表读取失败' }
      } else data = state.models
    }
    else if (url.pathname === '/api/license-info') data = { authorized: true, traffic_logs: false }
    else if (url.pathname === '/api/advertisements' || url.pathname.startsWith('/api/model-groups')) data = []
    else if (url.pathname === '/api/stats') data = url.searchParams.get('type') === 'daily' ? { daily: [] } : { credits: 1.25, total_requests: 1, uptime_seconds: 10 }
    else if (url.pathname === '/api/proxy-access-mode') data = { mode: 'local', host: '127.0.0.1' }
    else if (url.pathname === '/api/proxy-mode' || url.pathname === '/api/status') data = { mode: 'forward', status: 'running' }
    else if (url.pathname === '/api/service/status') data = { mode: 'forward', running: true, proxy_url: 'http://127.0.0.1:18081', ui_port: 18082, uptime_seconds: 10 }
    else if (url.pathname === '/api/ca-status') data = { installed: true, cert_exists: true }
    else if (url.pathname === '/api/kiro-proxy') data = { configured: true }
    else if (url.pathname === '/api/app-version') data = { version: 'mock' }
    else if (url.pathname !== '/api/update-info' && url.pathname !== '/api/heartbeat') {
      state.errors.push(`未定义 Mock：${request.method()} ${url.pathname}`)
      status = 500
      data = { error: '未定义 Mock' }
    }
    await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(data) })
  })
  const page = await context.newPage()
  page.on('pageerror', (error) => state.errors.push(error.message))
  return { context, page, state }
}

async function test(name, work) {
  await work()
  results.push(name)
  console.log(`PASS ${name}`)
}

async function visible(locator) { await locator.waitFor({ state: 'visible', timeout: 5000 }) }
async function hidden(locator) { await locator.waitFor({ state: 'hidden', timeout: 5000 }) }
function typesafeDialog(page) { return page.getByRole('dialog', { name: 'Typesafe classifier 设置', exact: true }) }
function autoDialog(page) { return page.getByRole('dialog', { name: 'Auto 按任务选模', exact: true }) }
function classifierChoice(page) { return page.locator('#classifier-model-choice') }
async function chooseTypesafe(page) {
  await classifierChoice(page).selectOption('__typesafe__')
}
async function assertBackground(page, locked) {
  assert.equal(await page.locator('#app').evaluate((el) => el.inert), locked)
  assert.equal(await page.locator('#app .scroll-stable').first().evaluate((el) => el.style.overflow === 'hidden'), locked)
}
async function assertFocusCycle(dialog, page) {
  const focusables = dialog.locator('button:not(:disabled), [href], input:not(:disabled), select:not(:disabled), textarea:not(:disabled)').filter({ visible: true })
  await focusables.first().focus()
  await page.keyboard.press('Shift+Tab')
  assert.equal(await focusables.last().evaluate((el) => el === document.activeElement), true)
  await page.keyboard.press('Tab')
  assert.equal(await focusables.first().evaluate((el) => el === document.activeElement), true)
}
async function assertBounds(dialog, page) {
  const box = await dialog.boundingBox()
  const viewport = page.viewportSize()
  assert.ok(box.x >= 0 && box.y >= 0 && box.x + box.width <= viewport.width + 1 && box.y + box.height <= viewport.height + 1)
  assert.ok(box.height <= viewport.height * 0.86 + 1)
  const save = dialog.getByRole('button', { name: /^保存/ }).first()
  const saveBox = await save.boundingBox()
  assert.ok(saveBox && saveBox.y >= box.y && saveBox.y + saveBox.height <= box.y + box.height)
}
async function waitWrites(state, count) {
  for (let i = 0; i < 100 && state.writes.length < count; i++) await new Promise((done) => setTimeout(done, 20))
  assert.equal(state.writes.length, count)
}
async function setAutoPolicy(dialog, enabled) {
  const input = dialog.getByRole('checkbox', { name: '启用按任务选模', exact: true })
  // 点击可见的 switch label，sr-only input 本身被装饰轨道覆盖。
  if (await input.isChecked() !== enabled) await input.locator('..').click()
  assert.equal(await input.isChecked(), enabled)
}
async function end({ context, state }) {
  assert.deepEqual(state.errors, [])
  await context.close()
}

try {
  browser = await chromium.launch({ headless: true })

  await test('首页紧凑入口、弹窗取消/焦点/背景锁与Key销毁', async () => {
    const f = await fixture()
    const { page, state } = f
    await page.goto(origin)
    const entry = page.getByRole('button', { name: '设置 Typesafe', exact: true })
    await visible(entry)
    assert.equal(await page.locator('input[type=password]').count(), 0)
    assert.equal(await page.locator('#auto-routing-default_model_id').count(), 0)
    assert.equal(await page.getByRole('dialog').count(), 0)
    assert.equal(Math.round((await page.locator('aside').last().boundingBox()).width), 280)
    await chooseTypesafe(page)
    await entry.click()
    const dialog = typesafeDialog(page)
    await visible(dialog)
    const key = dialog.locator('input[type=password]')
    assert.equal(await dialog.getByRole('checkbox').count(), 0)
    await key.waitFor()
    await page.waitForFunction(() => document.querySelector('[role=dialog] input[type=password]')?.value === '****')
    await assertBackground(page, true)
    await assertBounds(dialog, page)
    await assertFocusCycle(dialog, page)
    await key.fill('mock-unsaved-key')
    await page.keyboard.press('Escape')
    const confirm = page.getByRole('dialog', { name: '放弃未保存的修改？', exact: true })
    await visible(confirm)
    await assertFocusCycle(confirm, page)
    await confirm.getByRole('button', { name: '继续编辑', exact: true }).click()
    await hidden(confirm)
    assert.equal(await key.inputValue(), 'mock-unsaved-key')
    await dialog.getByRole('button', { name: '取消', exact: true }).click()
    await visible(confirm)
    await confirm.getByRole('button', { name: '放弃修改', exact: true }).click()
    await hidden(dialog)
    await assertBackground(page, false)
    assert.equal(await entry.evaluate((el) => el === document.activeElement), true)
    assert.equal(state.writes.length, 0)
    assert.equal(await page.locator('input[type=password]').count(), 0)
    await entry.click()
    await visible(dialog)
    await page.waitForFunction(() => document.querySelector('[role=dialog] input[type=password]')?.value === '****')
    await page.locator('[data-settings-overlay]').click({ position: { x: 5, y: 5 } })
    await hidden(dialog)
    await end(f)
  })

  await test('Typesafe保存键隔离、保存锁定、掩码重开与清除', async () => {
    const f = await fixture()
    const { page, state } = f
    await page.goto(origin)
    const entry = page.getByRole('button', { name: '设置 Typesafe', exact: true })
    await chooseTypesafe(page)
    await entry.click()
    const dialog = typesafeDialog(page)
    await visible(dialog)
    const key = dialog.locator('input[type=password]')
    await page.waitForFunction(() => document.querySelector('[role=dialog] input[type=password]')?.value === '****')
    await key.fill('mock-key-replacement')
    let release
    state.holdPut = new Promise((done) => { release = done })
    await dialog.getByRole('button', { name: '保存 Typesafe 设置', exact: true }).click()
    await waitWrites(state, 1)
    assert.equal(await key.isDisabled(), true)
    assert.equal(await dialog.getByRole('button', { name: '关闭设置', exact: true }).isDisabled(), true)
    await page.keyboard.press('Escape')
    await page.locator('[data-settings-overlay]').click({ position: { x: 5, y: 5 } })
    assert.equal(await dialog.isVisible(), true)
    release()
    state.holdPut = null
    await hidden(dialog)
    assert.deepEqual(Object.keys(state.writes[0]), ['typesafe_classifier_config'])
    assert.equal(JSON.parse(state.writes[0].typesafe_classifier_config).api_key, 'mock-key-replacement')
    await entry.click()
    await visible(dialog)
    await page.waitForFunction(() => document.querySelector('[role=dialog] input[type=password]')?.value === '****')
    assert.equal(await key.inputValue(), '****')
    await key.fill('')
    await dialog.getByRole('button', { name: '保存 Typesafe 设置', exact: true }).click()
    await hidden(dialog)
    assert.equal(JSON.parse(state.writes[1].typesafe_classifier_config).api_key, '')
    await end(f)
  })

  await test('Typesafe读取/提交/提交后重读失败均保持明确状态', async () => {
    const f = await fixture()
    const { page, state } = f
    await page.goto(origin)
    const entry = page.getByRole('button', { name: '设置 Typesafe', exact: true })
    await visible(entry)
    await chooseTypesafe(page)
    state.failGet = 1
    await entry.click()
    const dialog = typesafeDialog(page)
    await visible(dialog)
    await visible(dialog.getByRole('alert'))
    assert.equal(await dialog.getByRole('button', { name: '保存 Typesafe 设置', exact: true }).isDisabled(), true)
    await page.keyboard.press('Escape')
    await hidden(dialog)
    await entry.click()
    await visible(dialog)
    await page.waitForFunction(() => document.querySelector('[role=dialog] input[type=password]')?.value === '****')
    state.failPut = true
    await dialog.locator('input[type=password]').fill('mock-failed-key')
    await dialog.getByRole('button', { name: '保存 Typesafe 设置', exact: true }).click()
    await waitWrites(state, 1)
    await page.waitForFunction(() => document.querySelector('[role=dialog] input[type=password]')?.value === '')
    assert.equal(await dialog.isVisible(), true)
    state.failPut = false
    state.failReadAfterPut = true
    await dialog.locator('input[type=password]').fill('mock-saved-key')
    await dialog.getByRole('button', { name: '保存 Typesafe 设置', exact: true }).click()
    await visible(dialog.getByRole('alert'))
    assert.match(await dialog.getByRole('alert').innerText(), /已保存/)
    assert.equal(await dialog.locator('input[type=password]').inputValue(), '****')
    assert.equal(await dialog.getByRole('button', { name: '保存 Typesafe 设置', exact: true }).isDisabled(), true)
    assert.equal(await page.evaluate(() => JSON.stringify(localStorage) + JSON.stringify(sessionStorage)).then((value) => /mock-(failed|saved)-key/.test(value)), false)
    await dialog.getByRole('button', { name: '重新读取设置', exact: true }).click()
    await hidden(dialog.getByRole('alert'))
    assert.equal(await dialog.locator('input[type=password]').inputValue(), '****')
    assert.equal(state.writes.length, 2)
    await end(f)
  })

  await test('Auto固定首行、普通模型筛选/统计/分组隔离及零模型空态', async () => {
    const f = await fixture()
    const { page, state } = f
    await page.goto(`${origin}/#/models`)
    const row = page.locator('[data-auto-model-row]')
    await visible(row)
    assert.match(await row.innerText(), /内置默认配置/)
    assert.equal(await row.locator('..').evaluate((table) => table.children[1]?.hasAttribute('data-auto-model-row')), true)
    assert.equal(await row.getByRole('button').count(), 1)
    assert.equal(await row.locator('input').count(), 0)
    const search = page.getByPlaceholder('搜索模型名称、ID 或 Provider...')
    await search.fill('no-match')
    await visible(page.getByText('没有匹配当前筛选条件的模型', { exact: true }))
    assert.equal(await row.isVisible(), true)
    await search.fill('')
    const provider = page.locator('select').filter({ has: page.locator('option[value=gemini]') }).first()
    await provider.selectOption('gemini')
    await visible(page.getByText('没有匹配当前筛选条件的模型', { exact: true }))
    assert.equal(await row.isVisible(), true)
    await provider.selectOption('all')
    const status = page.locator('select').filter({ has: page.locator('option[value=disabled]') }).first()
    await status.selectOption('disabled')
    await visible(page.getByText('没有匹配当前筛选条件的模型', { exact: true }))
    assert.equal(await row.isVisible(), true)
    assert.equal(await page.getByText('模型总数', { exact: true }).locator('..').innerText().then((value) => value.includes('2')), true)
    await page.getByRole('button', { name: '分组', exact: true }).click()
    await hidden(row)
    await page.getByRole('button', { name: '模型', exact: true }).click()
    await visible(row)
    state.models = []
    await page.reload()
    await visible(row)
    await visible(page.getByText('暂无上游模型', { exact: true }))
    assert.equal(state.writes.length, 0)
    assert.equal(state.requests.some(([method]) => method !== 'GET'), false)
    await end(f)
  })

  await test('Auto弹窗保存/取消、策略关闭保留固定入口与保存失败', async () => {
    const f = await fixture()
    const { page, state } = f
    await page.goto(`${origin}/#/models`)
    const entry = page.getByRole('button', { name: '配置 Auto', exact: true })
    await entry.click()
    const dialog = autoDialog(page)
    await visible(dialog)
    const defaultModel = dialog.locator('#auto-routing-default_model_id')
    await defaultModel.waitFor()
    await assertBounds(dialog, page)
    await assertFocusCycle(dialog, page)
    await defaultModel.selectOption('m-default')
    await setAutoPolicy(dialog, true)
    let release
    state.holdPut = new Promise((done) => { release = done })
    await dialog.getByRole('button', { name: '保存选模设置', exact: true }).click()
    await waitWrites(state, 1)
    assert.equal(await defaultModel.isDisabled(), true)
    await page.keyboard.press('Escape')
    assert.equal(await dialog.isVisible(), true)
    release()
    state.holdPut = null
    await hidden(dialog)
    await assertBackground(page, false)
    assert.equal(await entry.evaluate((el) => el === document.activeElement), true)
    assert.deepEqual(Object.keys(state.writes[0]), ['auto_routing_config'])
    assert.equal(JSON.parse(state.writes[0].auto_routing_config).default_model_id, 'm-default')
    await entry.click()
    await visible(dialog)
    await page.waitForFunction(() => document.querySelector('#auto-routing-default_model_id')?.value === 'm-default')
    await setAutoPolicy(dialog, false)
    state.failPut = true
    await dialog.getByRole('button', { name: '保存选模设置', exact: true }).click()
    await waitWrites(state, 2)
    await dialog.getByRole('alert').filter({ hasText: 'Mock 保存失败' }).waitFor()
    assert.equal(await dialog.isVisible(), true)
    await page.keyboard.press('Escape')
    const confirm = page.getByRole('dialog', { name: '放弃未保存的修改？', exact: true })
    await visible(confirm)
    await confirm.getByRole('button', { name: '放弃修改', exact: true }).click()
    await hidden(dialog)
    assert.equal(JSON.parse(state.settings.auto_routing_config).enabled, true)
    state.failPut = false
    await entry.click()
    await visible(dialog)
    await page.waitForFunction(() => document.querySelector('#auto-routing-default_model_id')?.value === 'm-default')
    await setAutoPolicy(dialog, false)
    await dialog.getByRole('button', { name: '保存选模设置', exact: true }).click()
    await hidden(dialog)
    assert.equal(JSON.parse(state.settings.auto_routing_config).enabled, false)
    assert.equal(await page.locator('[data-auto-model-row]').isVisible(), true)
    await end(f)
  })

  await test('损坏Auto配置不伪报默认、不允许覆盖', async () => {
    const f = await fixture({ settings: { auto_routing_config: '{broken' } })
    const { page, state } = f
    await page.goto(`${origin}/#/models`)
    await visible(page.locator('[data-auto-model-row]'))
    await page.getByRole('button', { name: '配置 Auto', exact: true }).click()
    const dialog = autoDialog(page)
    await visible(dialog)
    await visible(dialog.getByRole('alert').first())
    assert.equal(await dialog.getByRole('button', { name: '保存选模设置', exact: true }).isDisabled(), true)
    assert.equal(state.writes.length, 0)
    await end(f)
  })

  await test('Classifier初始选择与实际Typesafe模型一致、按钮同行及切离停用', async () => {
    const config = { ...typesafeDefaults(), enabled: true, model: 'jev-configured' }
    const f = await fixture({ settings: { classifier_model_id: '__disabled__', typesafe_classifier_config: JSON.stringify(config) } })
    const { page, state } = f
    await page.goto(origin)
    const select = classifierChoice(page)
    await page.waitForFunction(() => document.querySelector('#classifier-model-choice')?.value === '__typesafe__')
    assert.equal(await select.isDisabled(), false)
    assert.match(await select.locator('option:checked').innerText(), /Typesafe classifier.*jev-configured/)
    const save = page.getByRole('button', { name: '保存设置', exact: true })
    const settings = page.getByRole('button', { name: '设置 Typesafe', exact: true })
    const saveBox = await save.boundingBox()
    const settingsBox = await settings.boundingBox()
    assert.equal(Math.round(saveBox.y), Math.round(settingsBox.y))
    assert.ok(saveBox.x + saveBox.width <= settingsBox.x)
    await select.selectOption('m-vision')
    assert.equal(await settings.isDisabled(), true)
    await visible(page.getByText(/待保存.*当前生效/))
    await save.click()
    await page.waitForFunction(() => document.querySelector('#classifier-model-choice')?.value === 'm-vision' && !document.querySelector('#classifier-model-choice')?.disabled)
    assert.equal(state.settings.classifier_model_id, 'm-vision')
    const persisted = JSON.parse(state.settings.typesafe_classifier_config)
    assert.equal(persisted.enabled, false)
    assert.equal(persisted.api_key, '****')
    assert.equal(persisted.model, 'jev-configured')
    assert.deepEqual(Object.keys(state.writes[0]).sort(), ['classifier_model_id', 'typesafe_classifier_config'])
    await page.reload()
    await page.waitForFunction(() => document.querySelector('#classifier-model-choice')?.value === 'm-vision')
    await end(f)
  })

  await test('模型列表读取失败仍准确显示已保存的Classifier ID', async () => {
    const f = await fixture({ failModels: true, settings: { classifier_model_id: 'm-default' } })
    const { page, state } = f
    await page.goto(origin)
    await page.waitForFunction(() => document.querySelector('#classifier-model-choice')?.value === 'm-default')
    assert.match(await classifierChoice(page).locator('option:checked').innerText(), /m-default.*列表中不可用/)
    assert.equal(state.writes.length, 0)
    state.failModels = false
    await page.reload()
    await page.waitForFunction(() => document.querySelector('#classifier-model-choice option:checked')?.textContent?.includes('Light'))
    assert.equal(await classifierChoice(page).locator('option').filter({ hasText: '列表中不可用' }).count(), 0)
    await end(f)
  })

  await test('Typesafe详情保存不启用、保留主选择草稿与动态模型标签', async () => {
    const f = await fixture({ settings: { classifier_model_id: 'm-default' } })
    const { page, state } = f
    await page.goto(origin)
    await chooseTypesafe(page)
    await page.getByRole('button', { name: '设置 Typesafe', exact: true }).click()
    const dialog = typesafeDialog(page)
    await visible(dialog)
    await page.waitForFunction(() => document.querySelector('[role=dialog] input[type=password]')?.value === '****')
    await dialog.getByLabel('模型', { exact: true }).fill('jev-new-config')
    await dialog.getByRole('button', { name: '保存 Typesafe 设置', exact: true }).click()
    await hidden(dialog)
    await page.waitForFunction(() => document.querySelector('#classifier-model-choice option:checked')?.textContent?.includes('jev-new-config'))
    assert.equal(await classifierChoice(page).inputValue(), '__typesafe__')
    assert.equal(JSON.parse(state.settings.typesafe_classifier_config).enabled, false)
    assert.equal(state.settings.classifier_model_id, 'm-default')
    assert.deepEqual(Object.keys(state.writes[0]), ['typesafe_classifier_config'])
    await visible(page.getByText(/待保存.*当前生效/))
    await page.getByRole('button', { name: '保存设置', exact: true }).click()
    await page.waitForFunction(() => document.querySelector('#classifier-model-choice')?.value === '__typesafe__' && !document.querySelector('#classifier-model-choice')?.disabled)
    assert.equal(JSON.parse(state.settings.typesafe_classifier_config).enabled, true)
    assert.equal(state.settings.classifier_model_id, 'm-default')
    assert.deepEqual(Object.keys(state.writes[1]), ['typesafe_classifier_config'])
    assert.equal(state.writes.some((payload) => payload.classifier_model_id === '__typesafe__'), false)
    await end(f)
  })

  await test('跟随与禁用选项保存同步关闭Typesafe、Auto配置不变', async () => {
    const config = { ...typesafeDefaults(), enabled: true }
    const f = await fixture({ settings: { classifier_model_id: 'm-default', typesafe_classifier_config: JSON.stringify(config) } })
    const { page, state } = f
    const autoBefore = state.settings.auto_routing_config
    await page.goto(origin)
    for (const choice of ['__disabled__', '', 'm-default']) {
      await classifierChoice(page).selectOption(choice)
      await page.getByRole('button', { name: '保存设置', exact: true }).click()
      await page.waitForFunction((value) => document.querySelector('#classifier-model-choice')?.value === value && !document.querySelector('#classifier-model-choice')?.disabled, choice)
      assert.equal(state.settings.classifier_model_id, choice)
      assert.equal(JSON.parse(state.settings.typesafe_classifier_config).enabled, false)
      // 等待短暂“已保存”标签恢复，避免把另一次提交当成本次完成。
      await visible(page.getByRole('button', { name: '保存设置', exact: true }))
    }
    assert.equal(state.settings.auto_routing_config, autoBefore)
    await end(f)
  })

  await test('无Key启用错误不改变生效选择、可配置后启用', async () => {
    const f = await fixture({ settings: { classifier_model_id: 'm-default', typesafe_classifier_config: JSON.stringify({ ...typesafeDefaults(), api_key: '' }) } })
    const { page, state } = f
    await page.goto(origin)
    await chooseTypesafe(page)
    await page.getByRole('button', { name: '保存设置', exact: true }).click()
    await visible(page.locator('#app').getByRole('alert').filter({ hasText: 'api_key is required' }))
    assert.equal(JSON.parse(state.settings.typesafe_classifier_config).enabled, false)
    assert.equal(state.settings.classifier_model_id, 'm-default')
    assert.equal(await classifierChoice(page).inputValue(), '__typesafe__')
    await page.getByRole('button', { name: '设置 Typesafe', exact: true }).click()
    const dialog = typesafeDialog(page)
    await visible(dialog)
    await dialog.locator('input[type=password]').fill('mock-configured-key')
    await dialog.getByRole('button', { name: '保存 Typesafe 设置', exact: true }).click()
    await hidden(dialog)
    assert.equal(JSON.parse(state.settings.typesafe_classifier_config).enabled, false)
    await page.getByRole('button', { name: '保存设置', exact: true }).click()
    await page.waitForFunction(() => document.querySelector('#classifier-model-choice')?.value === '__typesafe__' && !document.querySelector('#classifier-model-choice')?.disabled)
    assert.equal(JSON.parse(state.settings.typesafe_classifier_config).enabled, true)
    await end(f)
  })

  await test('主Classifier读取/提交/重读失败不伪报生效、重读恢复', async () => {
    const f = await fixture({ settings: { classifier_model_id: 'm-default' } })
    const { page, state } = f
    state.failGet = 1
    await page.goto(origin)
    const alert = page.locator('#app').getByRole('alert')
    await visible(alert)
    assert.equal(await classifierChoice(page).isDisabled(), true)
    assert.equal(await page.getByRole('button', { name: '保存设置', exact: true }).isDisabled(), true)
    assert.equal(state.writes.length, 0)
    await alert.getByRole('button', { name: /重新读取/ }).click()
    await hidden(alert)
    await classifierChoice(page).selectOption('m-vision')
    state.failPut = true
    await page.getByRole('button', { name: '保存设置', exact: true }).click()
    await visible(alert.filter({ hasText: 'Mock 保存失败' }))
    assert.equal(state.settings.classifier_model_id, 'm-default')
    assert.equal(await classifierChoice(page).inputValue(), 'm-vision')
    state.failPut = false
    state.failReadAfterPut = true
    await page.getByRole('button', { name: '保存设置', exact: true }).click()
    await visible(alert.filter({ hasText: '已保存' }))
    assert.equal(await classifierChoice(page).isDisabled(), true)
    assert.equal(state.settings.classifier_model_id, 'm-vision')
    state.failReadAfterPut = false
    await alert.getByRole('button', { name: /重新读取/ }).click()
    await hidden(alert)
    assert.equal(await classifierChoice(page).inputValue(), 'm-vision')
    assert.equal(state.writes.length, 2)
    await end(f)
  })

  await test('窄屏与低高度弹窗边界、底部操作固定', async () => {
    const f = await fixture()
    const { page } = f
    for (const size of [{ width: 1024, height: 600 }, { width: 390, height: 700 }]) {
      await page.setViewportSize(size)
      await page.goto(`${origin}/#/models`)
      await page.getByRole('button', { name: '配置 Auto', exact: true }).click()
      const dialog = autoDialog(page)
      await visible(dialog)
      await dialog.locator('#auto-routing-default_model_id').waitFor()
      await assertBounds(dialog, page)
      await dialog.getByRole('button', { name: '关闭设置', exact: true }).click()
      await hidden(dialog)
    }
    await page.goto(origin)
    await chooseTypesafe(page)
    await page.getByRole('button', { name: '设置 Typesafe', exact: true }).click()
    const dialog = typesafeDialog(page)
    await visible(dialog)
    await assertBounds(dialog, page)
    await end(f)
  })

  console.log(JSON.stringify({ passed: results.length, scenarios: results, screenshots: 0, realApiCalls: 0 }, null, 2))
} finally {
  await browser?.close()
  await new Promise((done) => server.close(done))
}
