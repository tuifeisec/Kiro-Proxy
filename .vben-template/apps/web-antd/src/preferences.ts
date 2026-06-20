import { defineOverridesPreferences } from '@vben/preferences';

export const overridesPreferences = defineOverridesPreferences({
  app: {
    checkUpdatesInterval: 0,
    defaultAvatar: '',
    defaultHomePath: '/dashboard',
    enableCheckUpdates: false,
    enableCopyPreferences: false,
    enablePreferences: false,
    name: import.meta.env.VITE_APP_TITLE,
  },
  copyright: {
    enable: false,
    settingShow: false,
  },
  logo: {
    enable: true,
    source: '',
  },
  shortcutKeys: {
    enable: false,
    globalEscape: false,
    globalLockScreen: false,
    globalLogout: false,
    globalPreferences: false,
    globalSearch: false,
  },
  tabbar: {
    showMaximize: false,
  },
  theme: {
    mode: 'light',
  },
  widget: {
    fullscreen: false,
    globalSearch: false,
    languageToggle: false,
    lockScreen: false,
    notification: false,
    refresh: true,
    sidebarToggle: true,
    themeToggle: false,
    timezone: false,
  },
});

export const preferencesExtension = undefined;
