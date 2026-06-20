import { resolve } from 'node:path';

import { defineConfig } from '@vben/vite-config';

export default defineConfig(async () => {
  return {
    application: {},
    vite: {
      build: {
        outDir: resolve(__dirname, '../../../go/web/admin/dist'),
        emptyOutDir: true,
      },
      server: {
        proxy: {
          '/api': {
            changeOrigin: true,
            target: 'http://localhost:20543',
            ws: true,
          },
        },
      },
    },
  };
});
