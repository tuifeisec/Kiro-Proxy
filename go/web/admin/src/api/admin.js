import { request } from './client.js'

export const login = (payload) => request('/api/admin/login', { method: 'POST', body: JSON.stringify(payload) })
export const logout = () => request('/api/admin/logout', { method: 'POST' })
export const getState = () => request('/api/admin/state')
export const authorizeMachine = (payload) => request('/api/admin/authorize', { method: 'POST', body: JSON.stringify(payload) })
export const addRelease = (payload) => request('/api/admin/releases', { method: 'POST', body: JSON.stringify(payload) })
export const deleteRelease = (version) => request(`/api/admin/releases?version=${encodeURIComponent(version)}`, { method: 'DELETE' })
export const saveAd = (payload) => request('/api/admin/ads', { method: 'POST', body: JSON.stringify(payload) })
export const deleteAd = (id) => request(`/api/admin/ads?id=${encodeURIComponent(id)}`, { method: 'DELETE' })
export const saveBranding = (payload) => request('/api/admin/branding', { method: 'PUT', body: JSON.stringify(payload) })
