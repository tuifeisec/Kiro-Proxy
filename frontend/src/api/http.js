// 统一请求封装：解析 JSON、统一错误（优先 data.error，兜底 HTTP status）、默认 30 秒超时。
// 不做重试、拦截器和缓存；非 2xx 或 2xx 但含 error 字段时抛 Error，网络异常保留原始错误。

const DEFAULT_TIMEOUT_MS = 30000

export async function request(path, options = {}) {
  const { signal, timeout = DEFAULT_TIMEOUT_MS, ...init } = options
  let res
  try {
    res = await fetch(path, { ...init, signal: signal ?? AbortSignal.timeout(timeout) })
  } catch (e) {
    // AbortSignal.timeout 触发的中断在不同浏览器表现为 TimeoutError 或 AbortError
    if (e?.name === 'AbortError' || e?.name === 'TimeoutError') throw new Error('请求超时')
    throw e
  }
  // 空响应体（如 204、空字符串）或非 JSON 响应返回 null，由调用方按业务契约处理
  const data = await res.json().catch(() => null)
  if (!res.ok || data?.error) {
    throw new Error(data?.error || `HTTP ${res.status}`)
  }
  return data
}

export function jsonInit(method, body) {
  const init = { method }
  if (body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(body)
  }
  return init
}

export function get(path) {
  return request(path)
}

export function post(path, body) {
  return request(path, jsonInit('POST', body))
}

export function put(path, body) {
  return request(path, jsonInit('PUT', body))
}

export function del(path) {
  return request(path, { method: 'DELETE' })
}
