import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 3000,
    middlewareMode: false,
    allowedHosts: [
      'localhost',
      '127.0.0.1',
      'omnira.devops.k3gsolutions.com.br'
    ],
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      }
    },
    hmr: {
      protocol: 'ws',
      host: 'localhost',
      port: 3000,
    }
  }
})
