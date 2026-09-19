# OMNIRA - Setup Nginx

Expor frontend React e backend API via nginx em `omnira.devops.k3gsolutions.com.br`.

## Quick Start

```bash
# 1. Build e rodar tudo
docker-compose up -d

# 2. Verificar status
docker ps
docker logs omnira-nginx

# 3. Acessar
http://omnira.devops.k3gsolutions.com.br (requer DNS ou /etc/hosts)
```

## Arquitetura

```
┌────────────────────────────────────────┐
│  omnira.devops.k3gsolutions.com.br      │
│  (nginx: 80, 443)                       │
├────────────────────────────────────────┤
│  Frontend (React)    │  API (Go)        │
│  port 3000 (dev)     │  port 8080       │
│  port 80 (prod)      │                  │
├────────────────────────────────────────┤
│  PostgreSQL (5432) | NATS (4222)        │
│  OpenTelemetry (4317)                   │
└────────────────────────────────────────┘
```

## DNS Setup (Local)

Para testar localmente, editar `/etc/hosts`:

```bash
sudo vim /etc/hosts

# Adicionar
127.0.0.1  omnira.devops.k3gsolutions.com.br
```

## Nginx Config

**web/nginx.conf** configura:

1. **Frontend Assets** (SPA)
   - Serve `/usr/share/nginx/html` (build estático)
   - Rota desconhecida → `/index.html` (React Router)
   - Cache 1 ano para assets com hash

2. **API Proxy**
   - `/api/*` → `http://api:8080/api/*`
   - Websocket support (Upgrade header)
   - X-Forwarded headers para backend

3. **Security Headers**
   - X-Frame-Options: SAMEORIGIN
   - X-Content-Type-Options: nosniff
   - X-XSS-Protection
   - Referrer-Policy

4. **Compression**
   - gzip para text/javascript/json

## Production Checklist

### SSL/TLS (HTTPS)

1. Obter certificado (Let's Encrypt)
   ```bash
   certbot certonly --standalone -d omnira.devops.k3gsolutions.com.br
   ```

2. Copiar certs para `certs/`
   ```bash
   mkdir -p certs
   cp /etc/letsencrypt/live/omnira.devops.k3gsolutions.com.br/fullchain.pem certs/
   cp /etc/letsencrypt/live/omnira.devops.k3gsolutions.com.br/privkey.pem certs/
   ```

3. Atualizar nginx.conf para HTTPS
   ```nginx
   listen 443 ssl http2;
   ssl_certificate /etc/nginx/certs/fullchain.pem;
   ssl_certificate_key /etc/nginx/certs/privkey.pem;
   ssl_protocols TLSv1.2 TLSv1.3;
   ssl_ciphers HIGH:!aNULL:!MD5;
   ```

4. Redirecionar HTTP → HTTPS
   ```nginx
   server {
     listen 80;
     return 301 https://$host$request_uri;
   }
   ```

### Logs

```bash
# Frontend errors
docker logs omnira-web

# Nginx access/error
docker logs omnira-nginx

# API
docker logs omnira-api

# Database
docker logs omnira-postgres
```

### Performance Monitoring

```bash
# Verificar status nginx
docker exec omnira-nginx nginx -t

# Reload nginx (sem downtime)
docker exec omnira-nginx nginx -s reload

# Monitor tráfego
docker stats omnira-nginx omnira-web omnira-api
```

## Troubleshooting

### Frontend não carrega

```bash
# Verificar build
docker exec omnira-web ls -la /usr/share/nginx/html

# Verificar logs
docker logs omnira-web

# Verificar nginx config
docker exec omnira-nginx nginx -t
```

### API connection refused

```bash
# Verificar se API está rodando
docker logs omnira-api

# Testar conectividade
docker exec omnira-nginx curl -v http://api:8080/healthz
```

### DNS não resolve

```bash
# Localhost testing
curl -H "Host: omnira.devops.k3gsolutions.com.br" http://localhost

# Ou editar /etc/hosts (local)
127.0.0.1  omnira.devops.k3gsolutions.com.br
```

## Environment Variables

**docker-compose.yml** já configura:

- `DB_HOST=postgres`
- `NATS_URL=nats://nats:4222`
- `OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4317`

Para customizar, criar `.env`:

```env
DB_PASSWORD=secure_password
API_PORT=8080
FRONTEND_BUILD_DIR=/usr/share/nginx/html
```

## Cleanup

```bash
# Remover containers
docker-compose down

# Remover volumes (cuidado!)
docker-compose down -v

# Remover images
docker-compose down --rmi all
```

## Documentação Relacionada

- Frontend: `web/README.md`
- Backend: `docs/architecture/`
- Kubernetes: `helm/`

---

**Deploy Status**: ✅ Pronto para desenvolvimento local
**Próximo**: Configurar HTTPS e DNS externo
