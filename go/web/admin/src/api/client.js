export async function request(url, options = {}) {
    const response = await fetch(url, {
        credentials: 'include',
        headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
        ...options
    })
    const data = await response.json().catch(() => ({}))
    if (!response.ok) {
        throw new Error(data.error || '请求失败')
    }
    return data
}
