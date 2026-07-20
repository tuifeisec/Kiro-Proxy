const BASE = '/api/request-usage'

export async function getRequestUsage(start, end, limit = 200) {
    const params = new URLSearchParams({ start, end, limit: String(limit) })
    const response = await fetch(`${BASE}?${params.toString()}`)
    const data = await response.json().catch(() => ({}))
    if (!response.ok) throw new Error(data.error || '请求统计加载失败')
    return data
}
