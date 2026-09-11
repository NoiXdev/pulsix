import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      // Lets `vite dev` talk to a locally running manager without CORS
      // wiring: the SPA calls same-origin `/api/...`, Vite forwards it.
      '/api': 'http://127.0.0.1:8080',
    },
  },
});
