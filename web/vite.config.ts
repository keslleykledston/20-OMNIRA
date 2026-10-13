import { defineConfig, type Plugin } from 'vite'
import react from '@vitejs/plugin-react'
import { resolve } from 'node:path'

// Backend for the /api proxy (dev server and `vite preview`). Override for e2e / other hosts.
const apiTarget = process.env.VITE_API_PROXY || 'http://localhost:8080'

// /admin/* is the second entry (admin.html), like the container's nginx does (nginx.conf): the dev server and `vite preview` would otherwise answer it with index.html.
const adminEntry = (): Plugin => {
  const rewrite = (req: { url?: string }, _res: unknown, next: () => void) => {
    if (req.url && /^\/admin(\/|\?|$)/.test(req.url)) req.url = '/admin.html'
    next()
  }
  return {
    name: 'omnira-admin-entry',
    configureServer: (server) => void server.middlewares.use(rewrite),
    configurePreviewServer: (server) => void server.middlewares.use(rewrite),
  }
}

export default defineConfig({
  plugins: [react(), adminEntry()],
  // Two entries of one build (ADR-0041): the agent app (index.html) and the administration console (admin.html, served under /admin/).
  build: {
    rollupOptions: { input: { main: resolve(__dirname, 'index.html'), admin: resolve(__dirname, 'admin.html') } },
  },
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
