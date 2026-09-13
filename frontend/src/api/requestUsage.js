import { get } from './http.js'

const BASE = '/api'

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
    return get(`${BASE}/request-usage?${params.toString()}`)
}
