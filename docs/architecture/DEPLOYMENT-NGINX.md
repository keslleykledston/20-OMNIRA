# Deploy e Exposição — Nginx

## Domínio público

```text
https://omnira.devops.k3gsolutions.com.br
```

O domínio é parte da baseline atual e deve ser usado nos ambientes de demonstração/entrega incremental.

## Responsabilidade externa

Antes do deploy público, infraestrutura deve garantir:
- DNS `A/AAAA` ou CNAME apontando para o host/LB correto;
- portas 80/443;
- certificado TLS válido;
- firewall/security group adequado.

## Topologia inicial

```text
Internet
   |
 HTTPS :443
   |
 Nginx
   |--------------------------|
   |                          |
 /, Next assets             /api/*
   |                          |
 Next.js                   Go API
                              |
                           Postgres
                              |
                            NATS
```

## Rotas públicas

```text
/                         -> frontend Next.js
/_next/*                  -> frontend Next.js
/api/v1/*                 -> Go API
/webhooks/v1/*            -> Go API
/realtime/v1/*            -> Go API/realtime
```

## Rotas proibidas publicamente

```text
/internal/*
/internal/metrics
/internal/health/modules
/debug/*
/pprof/*
```

Essas rotas devem permanecer em rede interna/admin.

## Health público

O load balancer/Nginx pode utilizar um endpoint mínimo dedicado, mas não deve expor diagnóstico detalhado.

## WebSocket

Nginx precisa suportar upgrade em `/realtime/v1/*`.

Conceito:

```nginx
proxy_http_version 1.1;
proxy_set_header Upgrade $http_upgrade;
proxy_set_header Connection "upgrade";
```

## Headers encaminhados

Preservar:
- Host;
- X-Forwarded-For;
- X-Forwarded-Proto;
- X-Request-ID quando presente.

A aplicação deve gerar correlation id se inexistente.

## TLS

Produção/demo pública usa HTTPS.

HTTP:
- redireciona para HTTPS.

## Cookies/Auth

Se autenticação utilizar cookie:
- Secure;
- HttpOnly quando aplicável;
- SameSite apropriado;
- domínio/host coerente com `omnira.devops.k3gsolutions.com.br`.

## Containers

Deploy inicial pode usar Docker Compose no servidor.

Serviços mínimos:

```text
nginx
web
api
worker
postgres
nats
otel-collector
```

Grafana/Prometheus/Loki/Tempo podem ficar em rede interna/admin.

## Princípio de entrega

Cada release deve poder ser publicada no mesmo domínio.

Não criar subdomínios diferentes para cada módulo do MVP sem necessidade.

## Configuração por ambiente

Frontend:
```text
NEXT_PUBLIC_APP_URL=https://omnira.devops.k3gsolutions.com.br
NEXT_PUBLIC_API_BASE=/api/v1
```

Backend:
```text
OMNIRA_PUBLIC_URL=https://omnira.devops.k3gsolutions.com.br
OMNIRA_HTTP_ADDR=:8080
```

## Segurança Nginx mínima

- body size configurado;
- timeouts;
- TLS moderno;
- server tokens ocultos quando possível;
- rate limiting seletivo em login/webhooks se compatível;
- não cachear respostas autenticadas indiscriminadamente;
- upload/media com limites explícitos.
