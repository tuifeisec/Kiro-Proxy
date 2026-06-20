import { useAccessStore } from '@vben/stores';

import { baseRequestClient } from '#/api/request';

async function request<T>(url: string, options: RequestInit = {}) {
    const response = await fetch(url, {
        credentials: 'include',
        headers: {
            'Content-Type': 'application/json',
            ...(options.headers || {}),
        },
        ...options,
    });
    const data = await response.json().catch(() => ({}));
    if (!response.ok) {
        if (response.status === 401) {
            const accessStore = useAccessStore();
            accessStore.setAccessToken(null);
            accessStore.setRefreshToken(null);
            accessStore.setAccessCodes([]);
            accessStore.setIsAccessChecked(false);

            const currentHash = window.location.hash || '#/dashboard';
            const redirect = currentHash.startsWith('#/auth/login') ? '' : `?redirect=${encodeURIComponent(currentHash)}`;
            window.location.replace(`${window.location.origin}/sjksjk/#/auth/login${redirect}`);
            return new Promise<T>(() => { });
        }
        throw new Error(data?.error || response.statusText);
    }
    return data as T;
}

export interface AuthorizationItem {
    machine_code: string;
    remark: string;
    enabled: boolean;
    traffic_logs: boolean;
    created_at: string;
    last_checked_at: string;
}

export interface ReleaseItem {
    platform: string;
    version: string;
    notes: string;
    download_url: string;
    expected_sha256: string;
    created_at: string;
}

export interface AdvertisementItem {
    id: string;
    title: string;
    link_url: string;
    image_url: string;
    description: string;
    enabled: boolean;
    created_at: string;
}

export interface Branding {
    app_name: string;
    unauthorized_text: string;
}

export interface AdminState {
    authorizations: Record<string, AuthorizationItem>;
    releases: ReleaseItem[];
    ads: AdvertisementItem[];
    branding: Branding;
}

export interface DashboardStats {
    authorization_count: number;
    recent_login_count: number;
    recent_authorization_count: number;
    daily: Array<{
        date: string;
        logins: number;
        authorizations: number;
    }>;
}

export const getState = () => request<AdminState>('/api/admin/state');
export const getDashboardStats = () =>
    request<DashboardStats>('/api/admin/dashboard');
export const authorizeMachine = (payload: Partial<AuthorizationItem>) =>
    request<AuthorizationItem>('/api/admin/authorize', {
        body: JSON.stringify(payload),
        method: 'POST',
    });
export const deleteAuthorization = (machineCode: string) =>
    request(`/api/admin/authorizations?machine_code=${encodeURIComponent(machineCode)}`, {
        method: 'DELETE',
    });
export const addRelease = (payload: Partial<ReleaseItem>) =>
    request<ReleaseItem>('/api/admin/releases', {
        body: JSON.stringify(payload),
        method: 'POST',
    });
export const deleteRelease = (platform: string, version: string) =>
    request(`/api/admin/releases?platform=${encodeURIComponent(platform)}&version=${encodeURIComponent(version)}`, {
        method: 'DELETE',
    });
export const saveAd = (payload: Partial<AdvertisementItem>) =>
    request<AdvertisementItem>('/api/admin/ads', {
        body: JSON.stringify(payload),
        method: 'POST',
    });
export const deleteAd = (id: string) =>
    request(`/api/admin/ads?id=${encodeURIComponent(id)}`, { method: 'DELETE' });
export const saveBranding = (payload: Branding) =>
    request<Branding>('/api/admin/branding', {
        body: JSON.stringify(payload),
        method: 'PUT',
    });

export const logout = () =>
    baseRequestClient.post('/api/admin/logout', undefined, { withCredentials: true });
