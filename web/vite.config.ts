import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Backend for the /api proxy (dev server and `vite preview`). Override for e2e / other hosts.
const apiTarget = process.env.VITE_API_PROXY || 'http://localhost:8080'

export default defineConfig({
  plugins: [react()],
  preview: {
    allowedHosts: ['localhost', '127.0.0.1'],
    proxy: { '/api': { target: apiTarget, changeOrigin: true } },
  },
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
        target: apiTarget,
        changeOrigin: true,
      }
    },
    hmr: {
      protocol: 'wss',
      host: 'omnira.devops.k3gsolutions.com.br',
      clientPort: 443,
    }
  }
})
