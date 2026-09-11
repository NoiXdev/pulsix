import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';
import path from 'node:path';

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@': path.resolve(import.meta.dirname, 'src'),
    },
  },
  server: {
    proxy: {
      // Lets `vite dev` talk to a locally running manager without CORS
      // wiring: the SPA calls same-origin `/api/...`, Vite forwards it.
      '/api': 'http://127.0.0.1:8080',
    },
  },
});
