import { baseRequestClient, requestClient } from '#/api/request';

export namespace AuthApi {
  export interface LoginParams {
    password?: string;
    username?: string;
  }

  export interface LoginResult {
    accessToken: string;
  }

  export interface RefreshTokenResult {
    data: string;
    status: number;
  }
}

export async function loginApi(data: AuthApi.LoginParams) {
  await baseRequestClient.post('/api/admin/login', data, {
    withCredentials: true,
  });
  return { accessToken: 'cookie-session' };
}

export async function refreshTokenApi() {
  return { data: 'cookie-session', status: 0 };
}

export async function logoutApi() {
  return baseRequestClient.post('/api/admin/logout', undefined, {
    withCredentials: true,
  });
}

export async function getAccessCodesApi() {
  return ['super'];
}
