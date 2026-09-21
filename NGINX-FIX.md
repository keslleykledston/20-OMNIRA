# Nginx Fix - Omnira via omnira.devops.k3gsolutions.com.br

## Problema

Acessar `omnira.devops.k3gsolutions.com.br/login` redirecionava para aplicação Netops (não relacionada).

## Causa

Não havia configuração nginx para `omnira.devops.k3gsolutions.com.br`. O nginx do sistema estava servindo uma aplicação padrão/catch-all (provavelmente Netops ou similar).

## Solução

### 1. Criar Configuração Nginx

Arquivo criado: `/etc/nginx/conf.d/omnira.conf`

**Configuração:**
- Listen na porta 80 (HTTP)
- Server name: `omnira.devops.k3gsolutions.com.br`
- Proxy frontend (React) em localhost:3000
- Proxy API em localhost:8080
- SPA routing automático (unknown routes → index.html)
- Gzip compression
- Security headers

### 2. Reload Nginx

```bash
sudo systemctl reload nginx
```

### 3. Iniciar Serviços

```bash
./start-omnira.sh
```

Ou manualmente:

**Terminal 1 - Backend:**
```bash
go run cmd/api/main.go
# Ou pre-built:
./bin/omnira-api
# Escuta em localhost:8080
```

**Terminal 2 - Frontend:**
```bash
cd web
npm install  # Primeira vez
npm run dev
# Escuta em localhost:3000
```

**Terminal 3 - PostgreSQL/NATS (se necessário):**
```bash
docker-compose up postgres nats
```

## Fluxo de Requisição

```
Browser: https://omnira.devops.k3gsolutions.com.br
         ↓
Nginx (80 → 3000)
         ↓
React Frontend (localhost:3000)
         ↓
Usuário clica "Login"
         ↓
Axios → /api/v1/auth/oidc/start  (ou /api/v1/auth/dev/login em ambiente de dev)
         ↓
Nginx (80 /api/* → 8080)
         ↓
Go API (localhost:8080)
         ↓
Response com JWT token
```

## Testar

### 1. Via CLI

```bash
# Check nginx
curl -I http://omnira.devops.k3gsolutions.com.br/
# Should return 200, HTML content

# Check API
curl -I http://omnira.devops.k3gsolutions.com.br/api/healthz
# Should return 200, JSON response
```

### 2. Via Browser

```
https://omnira.devops.k3gsolutions.com.br
→ Should show OMNIRA login page (não Netops)
```

### 3. Logs

```bash
# Nginx access
sudo tail -f /var/log/nginx/access.log | grep omnira

# Nginx error
sudo tail -f /var/log/nginx/error.log | grep omnira

# API logs
tail -f logs/api.log

# Frontend logs
tail -f logs/web.log
```

## SSL/HTTPS Setup (Próximo)

Para adicionar SSL:

```bash
# 1. Stop serviços
killall omnira-api
cd web && npm stop

# 2. Obter certificado (certbot vai pausar nginx temporariamente)
sudo certbot certonly --standalone -d omnira.devops.k3gsolutions.com.br

# 3. Atualizar omnira.conf com SSL
# (usar template em DEPLOY-NGINX.md)

# 4. Reload nginx
sudo systemctl reload nginx

# 5. Restart serviços
./start-omnira.sh
```

## Troubleshooting

### Frontend não carrega

```bash
# 1. Verificar se React está rodando
lsof -i :3000
# Deve mostrar processo npm

# 2. Verificar logs
tail -f logs/web.log

# 3. Testar direto
curl -I http://localhost:3000/
```

### API não responde

```bash
# 1. Verificar se Go está rodando
lsof -i :8080
# Deve mostrar processo omnira-api

# 2. Verificar logs
tail -f logs/api.log

# 3. Testar direto
curl -I http://localhost:8080/healthz
```

### Nginx não proxia

```bash
# 1. Test config
sudo nginx -t

# 2. Reload
sudo systemctl reload nginx

# 3. Check config
sudo cat /etc/nginx/conf.d/omnira.conf
```

## Arquivo de Configuração

```
/etc/nginx/conf.d/omnira.conf
↓
upstream omnira_backend → localhost:8080 (API)
upstream omnira_frontend → localhost:3000 (React)
↓
server {
  server_name omnira.devops.k3gsolutions.com.br
  listen 80
  
  /api/* → omnira_backend
  /* → omnira_frontend
}
```

## Scripts

| Script | Função |
|--------|--------|
| `start-omnira.sh` | Inicia todos os serviços (API + Frontend) |
| `scripts/deploy.sh` | Deploy com Docker Compose (buildado) |

## Status

✅ Nginx configurado
✅ Config testada e recarregada
⏳ Aguardando iniciar Backend + Frontend

---

**Próximo Passo**: 
```bash
./start-omnira.sh
# Ou iniciar Backend/Frontend manualmente
```

Então acesse: `https://omnira.devops.k3gsolutions.com.br`
