# Deploy OMNIRA via Nginx

Instância única com frontend React e backend API em `omnira.devops.k3gsolutions.com.br`.

## Arquivos de Configuração Criados

```
web/
├── Dockerfile          # Multi-stage build (node + nginx)
├── nginx.conf         # Nginx reverse proxy config
└── ...

scripts/
└── deploy.sh          # Deployment automation

docker-compose.yml    # Atualizado com web + nginx services
```

## Pré-requisitos

- Docker + Docker Compose
- Go 1.25+ (ou usar imagem Docker)
- Node.js 20+ (ou usar docker build)
- DNS resolvendo `omnira.devops.k3gsolutions.com.br` para seu IP

## Deploy Local (Teste)

### 1. Setup hosts (Linux/Mac)

```bash
sudo vim /etc/hosts
# Adicionar:
127.0.0.1  omnira.devops.k3gsolutions.com.br
```

### 2. Build e Start

```bash
# Opção A: Script automático
./scripts/deploy.sh

# Opção B: Manual
docker-compose up -d

# Verificar
docker-compose ps
```

### 3. Testar

```bash
# Frontend (com navegador)
open http://omnira.devops.k3gsolutions.com.br

# CLI
curl -i http://omnira.devops.k3gsolutions.com.br

# API
curl -i http://omnira.devops.k3gsolutions.com.br/api/healthz
```

## Deploy Produção

### 1. DNS (Configurar registros A/CNAME)

```dns
omnira.devops.k3gsolutions.com.br  A  <seu_ip_publico>
```

### 2. Certificado SSL (Let's Encrypt)

```bash
# Instalar certbot
sudo apt install certbot python3-certbot-nginx

# Obter certificado (certbot vai pausar nginx)
docker-compose down
sudo certbot certonly --standalone -d omnira.devops.k3gsolutions.com.br

# Copiar para projeto
mkdir -p certs
sudo cp /etc/letsencrypt/live/omnira.devops.k3gsolutions.com.br/fullchain.pem certs/
sudo cp /etc/letsencrypt/live/omnira.devops.k3gsolutions.com.br/privkey.pem certs/
sudo chown -R $(whoami) certs/
```

### 3. Atualizar nginx.conf para HTTPS

Editar `web/nginx.conf`:

```nginx
# Redirecionar HTTP → HTTPS
server {
  listen 80;
  server_name omnira.devops.k3gsolutions.com.br;
  return 301 https://$server_name$request_uri;
}

# HTTPS principal
server {
  listen 443 ssl http2;
  server_name omnira.devops.k3gsolutions.com.br;

  ssl_certificate /etc/nginx/certs/fullchain.pem;
  ssl_certificate_key /etc/nginx/certs/privkey.pem;
  ssl_protocols TLSv1.2 TLSv1.3;
  ssl_ciphers HIGH:!aNULL:!MD5;
  ssl_prefer_server_ciphers on;

  # ... resto da config
}
```

### 4. Start com SSL

```bash
# Portas 80 e 443
docker-compose up -d

# Verificar
curl -i https://omnira.devops.k3gsolutions.com.br
```

### 5. Auto-renew SSL (cron)

```bash
# Adicionar ao crontab (semanal)
0 3 * * 0 certbot renew --quiet && docker-compose restart nginx
```

## Arquitetura Resultante

```
┌─────────────────────────────────────────────────────┐
│          Nginx (omnira.devops.k3gsolutions.com.br)  │
│          80 (HTTP → HTTPS) | 443 (HTTPS)            │
├─────────────────────────────────────────────────────┤
│                                                      │
│  / (Frontend)              /api/* (Backend)         │
│  ↓                         ↓                         │
│  omnira-web container      omnira-api container     │
│  (React SPA)               (Go API)                  │
│                                                      │
├─────────────────────────────────────────────────────┤
│  omnira-postgres | omnira-nats | omnira-otel       │
└─────────────────────────────────────────────────────┘
```

## Monitoring & Logs

```bash
# Status
docker-compose ps

# Logs em tempo real
docker-compose logs -f

# Específico
docker logs omnira-nginx
docker logs omnira-api
docker logs omnira-web

# Acesso nginx
docker exec omnira-nginx tail -f /var/log/nginx/access.log

# Health checks
curl http://localhost:8080/healthz  # API
docker exec omnira-nginx curl http://api:8080/healthz
```

## Troubleshooting

### Nginx não encontra backend

```bash
# Verificar DNS dentro container
docker exec omnira-nginx nslookup api

# Verificar conectividade
docker exec omnira-nginx curl -v http://api:8080/healthz

# Verificar config
docker exec omnira-nginx nginx -t
```

### Frontend não carrega

```bash
# Verificar build
docker exec omnira-web ls -la /usr/share/nginx/html

# Verificar tamanho
docker exec omnira-web du -sh /usr/share/nginx/html

# Verificar React Router
docker exec omnira-web cat /usr/share/nginx/html/index.html | head -20
```

### Certificado expirando

```bash
# Verificar expiração
certbot certificates

# Renovar manual
sudo certbot renew --force-renewal
```

## Cleanup & Downtime

```bash
# Stop sem remover dados
docker-compose stop

# Start novamente
docker-compose start

# Remove tudo (cuidado!)
docker-compose down -v
rm -rf certs/

# Rebuild completo
docker-compose build --no-cache
docker-compose up -d
```

## Performance Tuning

```bash
# Aumentar nginx workers
# Em web/nginx.conf:
user nginx;
worker_processes auto;
worker_connections 2048;

# Aumentar node.js memory
# Em docker-compose.yml:
environment:
  - NODE_ENV=production
  - NODE_OPTIONS=--max-old-space-size=512
```

## Backup & Recovery

```bash
# Backup database
docker exec omnira-postgres pg_dump -U omnira omnira_dev > omnira_backup.sql

# Backup volumes
docker run --rm -v omnira_postgres_data:/data -v $(pwd):/backup \
  alpine tar czf /backup/postgres.tar.gz -C /data .

# Restore
docker-compose down
docker volume rm omnira_postgres_data
docker run --rm -v omnira_postgres_data:/data -v $(pwd):/backup \
  alpine tar xzf /backup/postgres.tar.gz -C /data
docker-compose up -d
```

## Documentação Relacionada

- **Frontend**: `web/README.md`
- **Nginx Config**: `web/nginx.conf`
- **Docker Compose**: `docker-compose.yml`
- **Backend API**: `docs/api/openapi.yaml`
- **Architecture**: `docs/architecture/`

---

**Status**: ✅ Pronto para deployment
**Próximo**: Configurar DNS e certificado SSL para produção

