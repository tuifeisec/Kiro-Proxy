const BASE = '/api/request-usage'

export async function getRequestUsage(start, end, options = {}) {
    const params = new URLSearchParams({
        start,
        end,
        page: String(options.page || 1),
        page_size: String(options.pageSize || 50),
    })
    for (const key of ['q', 'provider', 'status']) {
        if (options[key]) params.set(key, options[key])
    }
    const response = await fetch(`${BASE}?${params.toString()}`)
    const data = await response.json().catch(() => ({}))
    if (!response.ok) throw new Error(data.error || '请求统计加载失败')
    return data
}
