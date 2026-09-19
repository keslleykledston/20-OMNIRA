# Build & Deploy - OMNIRA Frontend

## Build Producão

```bash
npm run build
```

Gera arquivos otimizados em `dist/`:
- Bundle otimizado com code splitting
- Assets com hash de conteúdo
- TypeScript type-checked

## Docker Build

### Development
```bash
docker build -t omnira-web:dev .
docker run -p 3000:3000 omnira-web:dev
```

### Production
```bash
docker build -t omnira-web:prod .
docker run -p 80:80 omnira-web:prod
```

**Multi-stage build** (Node → Nginx):
1. Build stage: Node 20 compila TypeScript + Bundle
2. Serve stage: Nginx Alpine serve arquivos estáticos

## Nginx Setup

Configurado em `/etc/nginx/conf.d/00-omnira.conf`:

```nginx
server {
  listen 443 ssl;
  server_name omnira.devops.k3gsolutions.com.br;
  
  ssl_certificate /path/to/cert.pem;
  ssl_certificate_key /path/to/key.pem;
  
  location /api {
    proxy_pass http://localhost:8080;
  }
  
  location / {
    proxy_pass http://localhost:3000;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
  }
}
```

## HTTPS Setup (Self-Signed)

```bash
openssl req -x509 -newkey rsa:4096 \
  -keyout key.pem -out cert.pem \
  -days 365 -nodes \
  -subj "/CN=omnira.devops.k3gsolutions.com.br"
```

Copy certs:
```bash
sudo cp cert.pem /etc/nginx/ssl/
sudo cp key.pem /etc/nginx/ssl/
```

## Environment Variables

```bash
# .env.production
VITE_API_BASE_URL=https://omnira.devops.k3gsolutions.com.br/api
VITE_APP_NAME=OMNIRA
```

## CI/CD Integration

### GitHub Actions Example

```yaml
name: Build & Deploy

on:
  push:
    branches: [main]

jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-node@v3
        with:
          node-version: '20'
      
      - name: Install
        run: npm ci
      
      - name: Test
        run: npm test
      
      - name: E2E Tests
        run: npm run test:e2e
      
      - name: Build
        run: npm run build
      
      - name: Deploy
        run: npm run deploy
```

## Performance Checklist

- ✅ Code splitting (React Router lazy load)
- ✅ Image optimization (TailwindCSS)
- ✅ Gzip compression (Nginx)
- ✅ Cache headers (static assets)
- ✅ Source maps (development only)

## Health Check

```bash
# Check server
curl https://omnira.devops.k3gsolutions.com.br/

# Check API
curl https://omnira.devops.k3gsolutions.com.br/api/health

# Check Nginx
curl -I https://omnira.devops.k3gsolutions.com.br/
```

## Troubleshooting

### Port 3000 em uso
```bash
lsof -i :3000
kill -9 <PID>
```

### Certificado expirado
```bash
openssl x509 -in cert.pem -text -noout | grep -A2 Validity
```

### HMR websocket failed
Verificar `vite.config.ts`:
```typescript
hmr: {
  protocol: 'wss',
  host: 'omnira.devops.k3gsolutions.com.br',
  port: 443
}
```

## Status Atual

- ✅ Frontend React 18 + TypeScript
- ✅ Vite 5 com HMR WSS
- ✅ Nginx reverse proxy + HTTPS
- ✅ E2E tests com Playwright
- ✅ Unit tests com Vitest
- ✅ Docker multi-stage
- ✅ Mock API realista (5 contas, 5 tickets, 4 templates)

**Pronto para deployment em LIMITED_INTERNAL_PRODUCTION_CANDIDATE**.

---

**Status**: ✅ R1.0 COMPLETO
**Tickets**: T38-T45 IMPLEMENTADOS
**Coverage**: Auth, Dashboard, Contas, Tickets, Relatórios, Supervisor
