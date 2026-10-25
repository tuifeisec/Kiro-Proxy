// 前置：先构建 frontend/dist，再用 D:\nodejs\node.exe 执行；所有 HTTP API 均使用本地 Mock。
// 可设置 PLAYWRIGHT_MODULE 指向单独安装的 Playwright 包路径。
import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { createServer } from 'node:http'
import { readFile } from 'node:fs/promises'
import { dirname, extname, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright')
const scriptDir = dirname(fileURLToPath(import.meta.url))
const dist = resolve(scriptDir, '../dist')
const contentTypes = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml' }
const server = createServer(async (request, response) => {
  let pathname
  try { pathname = decodeURIComponent(new URL(request.url, 'http://localhost').pathname) } catch {
    response.writeHead(400).end()
    return
  }
  const file = resolve(dist, pathname === '/' ? 'index.html' : `.${pathname}`)
  if (!file.startsWith(`${dist}${sep}`)) {
    response.writeHead(403).end()
    return
  }
  try {
    const body = await readFile(file)
    if (response.destroyed) return
    response.writeHead(200, { 'Content-Type': contentTypes[extname(file)] || 'application/octet-stream' })
    response.end(body)
  } catch {
    if (!response.headersSent && !response.destroyed) response.writeHead(404).end()
  }
})

const results = []
const contexts = []
let browser
let origin

function fixtureState(options = {}) {
  return {
    mode: options.mode || 'capture',
    installation: options.installation || 'record_previous',
    verified: options.verified ?? false,
    bridgeMessage: options.bridgeMessage || '旧版安装记录只表示文件来源，不能证明运行中的 Kiro 已加载。',
    restartRequired: options.restartRequired ?? true,
    restartReason: options.restartReason || '代理模式或桥接设置需要由 Kiro 重启后应用。',
    diagnosticError: options.diagnosticError || '',
    restartResponse: options.restartResponse || { ok: true, message: 'Mock Kiro 重启完成' },
    holdRestart: options.holdRestart || false,
    restartBarrier: null,
    releaseRestart: null,
    restartRouted: null,
    notifyRestartRouted: null,
    restartCalls: 0,
    statusReads: 0,
    requests: [],
    errors: [],
  }
}

function kiroStatus(state) {
  return {
    configured: true,
    current_proxy: 'http://127.0.0.1:18081',
    proxy_mode: state.mode,
    stage_bridge: {
      required: state.mode === 'forward',
      reason: state.mode === 'capture' ? 'capture' : 'record_previous',
      installation: state.installation,
      message: state.bridgeMessage,
      adapter_sha256: 'fixture-adapter-hash',
      expected_adapter_sha256: 'fixture-expected-hash',
      bundle_sha256: 'fixture-bundle-hash',
      verified: state.verified,
      restart_required: state.restartRequired,
      restart_reason: state.restartReason,
      last_launch: {
        submitted_at: '2026-10-15T00:00:00Z',
        proxy_mode: state.mode,
        bridge_enabled: state.mode === 'forward',
        bundle_sha256: 'fixture-bundle-hash',
        adapter_sha256: 'fixture-adapter-hash',
      },
    },
  }
}

function proxyModeStatus(state) {
  return {
    mode: state.mode,
    kiro_restart_required: state.restartRequired,
    kiro_restart_reason: state.restartReason,
  }
}

async function fixture(options = {}) {
  const state = fixtureState(options)
  state.restartRouted = new Promise((done) => { state.notifyRestartRouted = done })
  if (state.holdRestart) {
    state.restartBarrier = new Promise((done) => { state.releaseRestart = done })
  }
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, reducedMotion: 'reduce' })
  contexts.push(context)
  context.setDefaultTimeout(7000)
  await context.route('**/*', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    if (url.origin !== origin) {
      state.errors.push(`阻止外部请求：${url.origin}`)
      await route.abort()
      return
    }
    if (!url.pathname.startsWith('/api/')) return route.continue()

    const method = request.method()
    const payload = request.postData() ? request.postDataJSON() : null
    state.requests.push({ method, path: url.pathname, payload })
    let data = {}
    let status = 200
    if (url.pathname === '/api/kiro-proxy') {
      if (method === 'GET') {
        state.statusReads++
        if (state.diagnosticError) {
          status = 503
          data = { error: state.diagnosticError }
        } else data = kiroStatus(state)
      } else if (method === 'POST') data = { ok: true, message: 'Mock configure complete' }
      else if (method === 'DELETE') data = { ok: true, message: 'Mock restore complete' }
    } else if (url.pathname === '/api/kiro-restart' && method === 'POST') {
      state.restartCalls++
      state.notifyRestartRouted?.()
      state.notifyRestartRouted = null
      if (state.restartBarrier) await state.restartBarrier
      data = state.restartResponse
      if (data.ok !== false) {
        state.installation = 'verified_current'
        state.verified = true
        state.bridgeMessage = '当前桥接文件已核验。'
        state.restartRequired = false
      }
    } else if (url.pathname === '/api/proxy-mode') {
      if (method === 'PUT') {
        state.mode = payload.mode
        state.restartRequired = true
        state.restartReason = '代理模式切换后需要重启 Kiro 以应用桥接设置。'
      }
      data = proxyModeStatus(state)
    } else if (url.pathname.startsWith('/api/model-groups')) data = []
    else if (url.pathname === '/api/models') data = []
    else if (url.pathname === '/api/settings') data = { classifier_model_id: '', auto_routing_config: '{}', typesafe_classifier_config: '{}' }
    else if (url.pathname === '/api/app-version') data = { version: 'mock' }
    else if (url.pathname === '/api/proxy-access-mode') data = { mode: 'local', host: '127.0.0.1', proxy_url: 'http://127.0.0.1:18081' }
    else if (url.pathname === '/api/log-detail') data = { mode: 'summary', max_body: 65536 }
    else if (url.pathname === '/api/stats') data = url.searchParams.get('type') === 'daily' ? { daily: [] } : { credits: 0, total_requests: 0, uptime_seconds: 10 }
    else if (url.pathname === '/api/request-usage' || url.pathname.startsWith('/api/logs')) data = { data: [], total: 0 }
    else if (url.pathname === '/api/status') data = { mode: state.mode, status: 'running' }
    else if (url.pathname === '/api/service/status') data = { mode: state.mode, running: true, proxy_url: 'http://127.0.0.1:18081', ui_port: 18082, uptime_seconds: 10 }
    else if (url.pathname === '/api/ca-status') data = { installed: true, cert_exists: true }
    else if (url.pathname === '/api/heartbeat') data = { ok: true }
    else {
      state.errors.push(`未定义 Mock：${method} ${url.pathname}`)
      status = 500
      data = { error: '未定义 Mock' }
    }
    await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(data) })
  })
  const page = await context.newPage()
  page.on('pageerror', (error) => state.errors.push(error.message))
  return { context, page, state }
}

async function scenario(name, options, work) {
  const f = await fixture(options)
  try {
    await work(f)
    assert.deepEqual(f.state.errors, [])
    results.push(name)
    console.log(`PASS ${name}`)
  } finally {
    f.state.releaseRestart?.()
    await f.context.close()
  }
}

function responseIs(method, pathname) {
  return (response) => response.request().method() === method && new URL(response.url()).pathname === pathname
}

async function testLifecycleTimeouts() {
  const api = await import('../src/api/settings.js')
  const originalFetch = globalThis.fetch
  const timeoutDescriptor = Object.getOwnPropertyDescriptor(AbortSignal, 'timeout')
  const calls = []
  Object.defineProperty(AbortSignal, 'timeout', {
    configurable: true,
    value: (milliseconds) => ({ mockTimeout: milliseconds }),
  })
  globalThis.fetch = async (url, init) => {
    calls.push({ url, method: init.method || 'GET', timeout: init.signal?.mockTimeout })
    return { ok: true, status: 200, json: async () => ({ ok: true, mode: 'forward' }) }
  }
  try {
    await api.configureKiroProxy()
    await api.restoreKiroProxy()
    await api.restartKiroProxy()
    await api.getProxyMode()
    await api.updateProxyMode('capture')
    assert.deepEqual(calls, [
      { url: '/api/kiro-proxy', method: 'POST', timeout: 180000 },
      { url: '/api/kiro-proxy', method: 'DELETE', timeout: 180000 },
      { url: '/api/kiro-restart', method: 'POST', timeout: 180000 },
      { url: '/api/proxy-mode', method: 'GET', timeout: 30000 },
      { url: '/api/proxy-mode', method: 'PUT', timeout: 30000 },
    ])

    let failedCalls = 0
    const originalError = new Error('Mock network failure')
    globalThis.fetch = async () => { failedCalls++; throw originalError }
    await assert.rejects(api.restartKiroProxy(), (error) => error === originalError)
    assert.equal(failedCalls, 1, 'lifecycle request must not retry after a timeout or network failure')
    results.push('API lifecycle 180000ms; ordinary requests 30000ms; failure is not retried')
    console.log('PASS API lifecycle timeout and no-retry contract')
  } finally {
    globalThis.fetch = originalFetch
    if (timeoutDescriptor) Object.defineProperty(AbortSignal, 'timeout', timeoutDescriptor)
    else delete AbortSignal.timeout
  }
}

try {
  await new Promise((done, fail) => {
    server.once('error', fail)
    server.listen(0, '127.0.0.1', done)
  })
  origin = `http://127.0.0.1:${server.address().port}`
  browser = await chromium.launch({ headless: true })
  await testLifecycleTimeouts()

  await scenario('dashboard shows compact Kiro status without bridge details', {}, async ({ page }) => {
    await page.goto(origin)
    const card = page.getByRole('heading', { name: 'Kiro 代理配置' }).locator('..').locator('..')
    await card.getByText('已配置', { exact: true }).waitFor({ state: 'visible' })
    await card.getByText('抓包模式', { exact: true }).waitFor({ state: 'visible' })
    await card.getByText('需重启 Kiro', { exact: true }).waitFor({ state: 'visible' })
    const text = await card.innerText()
    assert.equal((text.match(/已配置/g) || []).length, 1)
    assert.doesNotMatch(text, /桥接文件|已加载|官方流量进入官方接口/)
    if (process.env.KIRO_COMPACT_PREVIEW_PATH) {
      await page.setViewportSize({ width: 1193, height: 414 })
      await card.screenshot({ path: process.env.KIRO_COMPACT_PREVIEW_PATH })
    }
  })

  await scenario('unverified record stays concise without claiming runtime loading', {
    installation: 'record_current',
    verified: false,
    bridgeMessage: '当前安装记录没有可信收据，无法确认运行中的 Kiro 已加载。',
  }, async ({ page }) => {
    await page.goto(origin)
    const card = page.getByRole('heading', { name: 'Kiro 代理配置' }).locator('..').locator('..')
    await card.getByText('需重启 Kiro', { exact: true }).waitFor({ state: 'visible' })
    assert.doesNotMatch(await card.innerText(), /桥接文件|可信收据|已加载|安装预检/)
    await card.getByRole('button', { name: '转发并重启 Kiro', exact: true }).waitFor({ state: 'visible' })
  })

  await scenario('bridge diagnostic error has a short visible prompt', {
    installation: 'error',
    bridgeMessage: 'Mock 桥接文件读取原始错误',
  }, async ({ page }) => {
    await page.goto(origin)
    const card = page.getByRole('heading', { name: 'Kiro 代理配置' }).locator('..').locator('..')
    const prompt = card.getByText('配置需检查', { exact: true })
    await prompt.waitFor({ state: 'visible' })
    assert.equal(await prompt.getAttribute('title'), 'Mock 桥接文件读取原始错误')
    assert.doesNotMatch(await card.innerText(), /桥接文件|Mock 桥接文件读取原始错误/)
  })

  await scenario('diagnostic GET failure displays unknown state and the original error', {
    diagnosticError: 'Mock 诊断读取原始错误',
  }, async ({ page }) => {
    await page.goto(origin)
    await page.getByRole('alert').filter({ hasText: 'Kiro 状态未知：Mock 诊断读取原始错误' }).waitFor({ state: 'visible' })
    await page.getByText('状态未知', { exact: true }).waitFor({ state: 'visible' })
  })

  await scenario('dashboard switches mode before restart, locks duplicates, and refreshes after success', {
    holdRestart: true,
  }, async ({ page, state }) => {
    await page.goto(origin)
    const action = page.getByRole('button', { name: '转发并重启 Kiro', exact: true })
    await action.waitFor({ state: 'visible' })
    const firstActionRequest = state.requests.length
    const modeRequest = page.waitForRequest((request) => request.method() === 'PUT' && new URL(request.url()).pathname === '/api/proxy-mode')
    const restartRequest = page.waitForRequest((request) => request.method() === 'POST' && new URL(request.url()).pathname === '/api/kiro-restart')
    await action.click()
    await Promise.all([modeRequest, restartRequest])
    await state.restartRouted
    assert.deepEqual(state.requests.slice(firstActionRequest)
      .filter(({ method, path }) => (method === 'PUT' && path === '/api/proxy-mode') || (method === 'POST' && path === '/api/kiro-restart'))
      .map(({ method, path }) => [method, path]), [
      ['PUT', '/api/proxy-mode'], ['POST', '/api/kiro-restart'],
    ])
    assert.equal(state.mode, 'forward')
    assert.equal(await page.getByRole('button', { name: '处理中...', exact: true }).isDisabled(), true)
    const refresh = page.waitForResponse(responseIs('GET', '/api/kiro-proxy'))
    state.releaseRestart()
    await refresh
    await page.getByText('转发模式', { exact: true }).waitFor({ state: 'visible' })
    await page.getByText('需重启 Kiro', { exact: true }).waitFor({ state: 'hidden' })
    assert.equal(state.restartCalls, 1)
    assert.equal(state.statusReads, 2)
  })

  await scenario('restart failure keeps forward mode and shows the raw error as a failure', {
    restartResponse: { ok: false, message: 'Mock 重启原始错误' },
  }, async ({ page, state }) => {
    await page.goto(origin)
    await page.getByRole('button', { name: '转发并重启 Kiro', exact: true }).click()
    await page.getByRole('alert').filter({ hasText: 'Mock 重启原始错误' }).waitFor({ state: 'visible' })
    assert.equal(state.mode, 'forward')
    assert.equal(state.restartCalls, 1)
    assert.equal(state.requests.filter(({ method, path }) => method === 'POST' && path === '/api/kiro-restart').length, 1)
    const failure = page.getByRole('alert').filter({ hasText: 'Mock 重启原始错误' })
    const className = await failure.getAttribute('class')
    assert.match(className, /danger/)
    assert.doesNotMatch(className, /success/)
  })

  await scenario('Inspector keeps restart notice after mode change and preserves restart failure', {
    restartRequired: false,
    restartResponse: { ok: false, message: 'Mock 重启原始错误' },
  }, async ({ page, state }) => {
    await page.goto(`${origin}/#/inspector`)
    await page.getByRole('button', { name: '设置', exact: true }).click()
    await page.getByRole('button', { name: '转发模式', exact: true }).click()
    const notice = page.getByRole('status').filter({ hasText: '需要重启 Kiro' })
    await notice.waitFor({ state: 'visible' })
    const restart = page.getByRole('button', { name: '重启 Kiro 以应用模式', exact: true })
    await restart.waitFor({ state: 'visible' })
    assert.equal(state.mode, 'forward')
    assert.equal(state.restartRequired, true)
    await restart.click()
    await page.getByRole('alert').filter({ hasText: 'Mock 重启原始错误' }).waitFor({ state: 'visible' })
    await notice.waitFor({ state: 'visible' })
    assert.equal(state.restartCalls, 1)
    assert.equal(state.restartRequired, true)
  })

  await scenario('dashboard skips Kiro status refresh when unmounted during pending restart', {
    holdRestart: true,
    restartResponse: { ok: false, message: 'Mock 重启原始错误' },
  }, async ({ page, state }) => {
    await page.goto(origin)
    await page.getByRole('button', { name: '转发并重启 Kiro', exact: true }).click()
    await state.restartRouted
    const statusReadsBeforeNavigation = state.statusReads
    await page.getByRole('link', { name: '模型配置', exact: true }).click()
    await page.waitForURL(`${origin}/#/models`)
    await page.getByRole('heading', { name: '模型配置', exact: true }).waitFor({ state: 'visible' })
    const restartResponse = page.waitForResponse(responseIs('POST', '/api/kiro-restart'))
    state.releaseRestart()
    await restartResponse
    await page.waitForTimeout(100)
    assert.equal(state.statusReads, statusReadsBeforeNavigation, 'unmounted dashboard must not refetch /api/kiro-proxy')
    assert.equal(state.requests.filter(({ method, path }) => method === 'GET' && path === '/api/kiro-proxy').length, statusReadsBeforeNavigation)
  })

  await scenario('dashboard Kiro action labels fit at 1440px, 1193px and 900px', {}, async ({ page, state }) => {
    await page.goto(origin)
    async function checkLabel(label, width) {
      await page.setViewportSize({ width, height: 900 })
      const button = page.getByRole('button', { name: label, exact: true })
      await button.waitFor({ state: 'visible' })
      const metrics = await button.evaluate((element) => {
        const box = element.getBoundingClientRect()
        const range = document.createRange()
        range.selectNodeContents(element)
        const textRects = [...range.getClientRects()].filter((rect) => rect.width > 0)
        return {
          width: element.clientWidth,
          scrollWidth: element.scrollWidth,
          height: element.clientHeight,
          scrollHeight: element.scrollHeight,
          textLines: new Set(textRects.map((rect) => Math.round(rect.top))).size,
          textOutsideButton: textRects.some((rect) => rect.top < box.top - 1 || rect.bottom > box.bottom + 1 || rect.left < box.left - 1 || rect.right > box.right + 1),
          pageOverflows: document.documentElement.scrollWidth > document.documentElement.clientWidth,
        }
      })
      assert.equal(metrics.textLines, 1, `${label} wraps at ${width}px: ${JSON.stringify(metrics)}`)
      assert.ok(metrics.scrollWidth <= metrics.width + 1, `${label} overflows horizontally at ${width}px: ${JSON.stringify(metrics)}`)
      assert.ok(metrics.scrollHeight <= metrics.height + 1, `${label} overflows vertically at ${width}px: ${JSON.stringify(metrics)}`)
      assert.equal(metrics.textOutsideButton, false, `${label} text escapes its button at ${width}px`)
      assert.equal(metrics.pageOverflows, false, `page has horizontal overflow at ${width}px`)
      console.log(`LAYOUT ${width}px ${label}: ${JSON.stringify(metrics)}`)
    }

    await checkLabel('转发并重启 Kiro', 1440)
    await checkLabel('转发并重启 Kiro', 1193)
    await checkLabel('转发并重启 Kiro', 900)
    state.mode = 'forward'
    await page.reload()
    await checkLabel('更新并重启 Kiro', 1440)
    await checkLabel('更新并重启 Kiro', 1193)
    await checkLabel('更新并重启 Kiro', 900)
  })

  console.log(JSON.stringify({ passed: results.length, scenarios: results, browser: 'Chromium', realApiCalls: 0 }, null, 2))
} finally {
  for (const context of contexts) await context.close().catch(() => {})
  await browser?.close()
  await new Promise((done) => server.close(done))
}
