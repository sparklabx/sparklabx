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
            // Vite's virtual helper modules (preload helper, commonjs
            // helpers) are shared by every chunk that uses dynamic import.
            // Left unassigned, Rollup hoists them into vendor-monaco —
            // making the tiny helper drag the whole 3.7MB Monaco chunk into
            // the entry's static imports (and a modulepreload on login).
            // Pin them next to React, which the entry always loads anyway.
            if (id.includes('\0vite/') || id.includes('commonjsHelpers')) {
              return 'vendor-react';
            }
            if (id.includes('node_modules')) {
              // Anchor React in its own chunk. Without this, Rollup hoists
              // react/react-dom INTO the vendor-monaco manual chunk (first
              // manual chunk that depends on them), which makes the entry
              // statically import — and the browser preload — all of Monaco
              // on the login screen.
              if (/node_modules\/(react|react-dom|scheduler)\//.test(id)) {
                return 'vendor-react';
              }
              // Monaco is huge and only needed by the notebook editor —
              // keep it in its own long-cacheable chunk. Match the package
              // dir exactly: a bare 'monaco' substring also catches
              // @monaco-editor/react and drags shared deps along.
              if (id.includes('node_modules/monaco-editor/')) return 'vendor-monaco';
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
