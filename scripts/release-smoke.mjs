// 在一次性数据目录启动已打包的桌面二进制，验证无需激活即可使用 UI、API 与日志 WS。
import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { mkdtemp, readFile } from 'node:fs/promises'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'

const [binary, expectedVersion, expectedRevision] = process.argv.slice(2)
assert.ok(binary && expectedVersion && expectedRevision, '用法：node scripts/release-smoke.mjs 二进制 版本 提交')
const executable = path.resolve(binary)
const dataDir = await mkdtemp(path.join(os.tmpdir(), 'kiro-release-smoke-'))

async function reservePorts() {
  for (let attempt = 0; attempt < 20; attempt++) {
    const proxy = net.createServer()
    const ui = net.createServer()
    try {
      await new Promise((resolve, reject) => {
        proxy.once('error', reject)
        proxy.listen(0, '127.0.0.1', resolve)
      })
      const port = proxy.address().port
      if (port >= 65535) continue
      await new Promise((resolve, reject) => {
        ui.once('error', reject)
        ui.listen(port + 1, '127.0.0.1', resolve)
      })
      return port
    } catch (error) {
      if (error.code !== 'EADDRINUSE') throw error
    } finally {
      await Promise.all([proxy, ui].map(server => new Promise(resolve => server.close(resolve))))
    }
  }
  throw new Error('无法预留相邻测试端口。')
}

const port = await reservePorts()
const childEnv = { ...process.env, HOME: dataDir, KIRO_LICENSE_SERVER: 'http://127.0.0.1:1' }
if (process.platform === 'win32') {
  const pathKey = Object.keys(childEnv).find(key => key.toLowerCase() === 'path') || 'PATH'
  // 隔离构建工具链，避免 PATH 中的 MinGW/MSYS DLL 掩盖发行包缺失依赖。
  childEnv[pathKey] = (childEnv[pathKey] || '').split(path.delimiter)
    .filter(entry => !/(?:mingw|msys|winlibs|\\Git\\)/i.test(entry)).join(path.delimiter)
}
const child = spawn(executable, [
  '-no-window', '-port', String(port), '-db', path.join(dataDir, 'smoke.db'),
  '-config', path.join(dataDir, 'missing-config.yaml'),
], {
  cwd: dataDir,
  env: childEnv,
  stdio: ['ignore', 'pipe', 'pipe'],
  windowsHide: true,
})
let output = ''
let spawnError
child.on('error', error => { spawnError = error })
child.stdout.on('data', data => { output += data })
child.stderr.on('data', data => { output += data })
const closed = new Promise(resolve => child.once('close', resolve))
const baseURL = `http://127.0.0.1:${port + 1}`
try {
  let version
  let lastError
  for (let attempt = 0; attempt < 100; attempt++) {
    if (spawnError) throw spawnError
    if (child.exitCode !== null || child.signalCode !== null) {
      throw new Error(`程序提前退出：${child.exitCode ?? child.signalCode}\n${output}`)
    }
    try {
      const response = await fetch(`${baseURL}/api/app-version`, { signal: AbortSignal.timeout(1000) })
      assert.equal(response.status, 200)
      version = await response.json()
      break
    } catch (error) {
      lastError = error
      await delay(200)
    }
  }
  assert.ok(version, `服务启动失败：${lastError}\n${output}`)
  assert.equal(version.version, expectedVersion)
  assert.equal(version.build.revision, expectedRevision)
  assert.equal(version.build.os, process.platform === 'win32' ? 'windows' : process.platform)
  assert.equal(version.build.arch, process.arch === 'x64' ? 'amd64' : process.arch)
  const index = await fetch(baseURL, { signal: AbortSignal.timeout(3000) })
  assert.equal(index.status, 200)
  const html = await index.text()
  const entry = html.match(/<script[^>]*src="([^"]+)"/)
  assert.ok(entry, '嵌入 index.html 缺少脚本入口。')
  const asset = await fetch(new URL(entry[1], `${baseURL}/`), { signal: AbortSignal.timeout(3000) })
  assert.equal(asset.status, 200)
  assert.match(asset.headers.get('content-type') || '', /javascript/)
  const logs = await fetch(`${baseURL}/api/logs`, { signal: AbortSignal.timeout(3000) })
  assert.equal(logs.status, 200, '日志接口不应依赖远程激活服务。')
  const socket = new WebSocket(`ws://127.0.0.1:${port + 1}/ws`)
  try {
    await Promise.race([
      once(socket, 'open'),
      once(socket, 'error').then(([event]) => { throw event.error || new Error('日志 WS 连接失败。') }),
      delay(5000).then(() => { throw new Error('日志 WS 连接超时。') }),
    ])
  } finally {
    socket.close()
  }
  console.log(JSON.stringify({ smoke: 'passed', version: expectedVersion, revision: expectedRevision, platform: process.platform, arch: process.arch }))
} catch (error) {
  console.error(output)
  for (const logPath of [path.join(dataDir, 'kiro-proxy.log'), path.join(dataDir, 'Library/Application Support/Kiro Proxy/kiro-proxy.log')]) {
    try { console.error(await readFile(logPath, 'utf8')) } catch {}
  }
  throw error
} finally {
  if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM')
  await Promise.race([closed, delay(5000).then(() => child.kill('SIGKILL'))])
  console.log(`Smoke 数据和日志保留于：${dataDir}`)
}