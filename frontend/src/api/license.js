export async function getLicenseInfo() {
    const response = await fetch('/api/license-info')
    if (!response.ok) throw new Error('获取授权状态失败')
    return response.json()
}

export async function getAdvertisements() {
    const response = await fetch('/api/advertisements')
    if (!response.ok) throw new Error('获取广告失败')
    return response.json()
}

export async function getAppVersion() {
    const response = await fetch('/api/app-version')
    if (!response.ok) throw new Error('获取版本号失败')
    return response.json()
}

export async function getUpdateInfo() {
    const response = await fetch('/api/update-info')
    if (!response.ok) throw new Error('获取更新信息失败')
    return response.json()
}

export async function openExternalURL(url) {
    const response = await fetch('/api/open-external-url', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ url }),
    })
    if (!response.ok) throw new Error('打开下载地址失败')
    return response.json()
}
