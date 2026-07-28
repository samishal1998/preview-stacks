import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';

export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5273,
    /**
     * Dev-mode stand-in for the nginx `/api/` proxy in the Dockerfile. The API sends no CORS
     * headers — deliberately, it is not a public surface — and `EventSource` cannot be given
     * headers or opted out of the same-origin rule either. So in dev we proxy rather than point
     * the client at `http://localhost:7878` and watch every request fail preflight.
     *
     *     PSTACK_TOKEN=… pstack serve      # 127.0.0.1:7878
     *     bun run dev                      # 127.0.0.1:5273, /api/* → 7878
     */
    proxy: {
      '/api': {
        target: process.env.PSTACK_API ?? 'http://127.0.0.1:7878',
        changeOrigin: true,
        // SSE: without this the live job log is buffered and arrives in one lump at the end.
        ws: false,
        configure: (proxy) => {
          proxy.on('proxyRes', (proxyRes) => {
            if (proxyRes.headers['content-type']?.includes('text/event-stream')) {
              proxyRes.headers['x-accel-buffering'] = 'no';
            }
          });
        },
      },
    },
  },
  build: {
    outDir: 'dist',
    // A control-plane UI is read by an operator debugging a broken host; a stack trace that names
    // real functions is worth the extra files, which are never served unless a devtools asks.
    sourcemap: true,
  },
});
