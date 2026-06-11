import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import svgr from 'vite-plugin-svgr';
import tsconfigPaths from 'vite-tsconfig-paths';
import tailwindcss from '@tailwindcss/vite';

const apiTarget = process.env.VITE_API_TARGET || 'http://localhost:10000';
console.log(`[vite] API proxy target: ${apiTarget}`);

export default defineConfig(() => {

  return {
    plugins: [
      react(),
      tailwindcss(),
      svgr(),
      tsconfigPaths(),
    ],
    resolve: {
      dedupe: ['react', 'react-dom'],
    },
    server: {
      port: 3000,
      host: true,
      proxy: {
        '/api': {
          target: apiTarget,
          changeOrigin: true,
          ws: true,
        },
      },
    },
    esbuild: {
      drop: ['console', 'debugger'],
    },
    build: {
      outDir: 'build',
      sourcemap: false,
      chunkSizeWarningLimit: 2000,
      rollupOptions: {
        output: {
          manualChunks(id) {
            if (id.includes('node_modules')) {
              // Monaco is huge and only needed by the notebook editor —
              // keep it in its own long-cacheable chunk.
              if (id.includes('monaco')) return 'vendor-monaco';
              // Everything else: let Rollup split along dynamic-import
              // boundaries. The previous catch-all 'vendor' chunk forced
              // the login screen to download @jupyterlab/services,
              // md-editor, hyparquet etc. before first paint.
              return undefined;
            }
          },
        },
      },
    },
    define: {
      'process.env': {},
    },
  };
});
