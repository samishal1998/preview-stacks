import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';

// Same dev shape as apps/ui: the API sends no CORS headers, so /api is proxied to the server.
//     PSTACK_TOKEN=… pstack serve      # 127.0.0.1:7878
//     bun run dev                      # 127.0.0.1:5274, /api/* → 7878
export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5274,
    proxy: { '/api': { target: process.env.PSTACK_API ?? 'http://127.0.0.1:7878', changeOrigin: true } },
  },
  build: { outDir: 'dist', sourcemap: true },
});
