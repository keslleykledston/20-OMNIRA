# OMNIRA × DeskcommCRM — Reuse Kit

Este pacote orienta Claude/Codex a reaproveitar de forma controlada partes do projeto:

`https://github.com/melgarafael/DeskcommCRM`

no projeto OMNIRA.

## Objetivo

Acelerar o OMNIRA sem transformar o projeto em um fork do DeskcommCRM.

O DeskcommCRM deve ser tratado como **donor/reference project**.

Classificação obrigatória para cada item analisado:

```text
COPY     = pode ser reutilizado quase diretamente
ADAPT    = código reaproveitável com adaptação para OMNIRA
PORT     = lógica deve ser traduzida para Go
INSPIRE  = regra/design útil, mas implementação não deve ser copiada
REJECT   = incompatível com as decisões do OMNIRA
```

## Decisões do OMNIRA que NÃO mudam

- Backend principal: Go.
- Frontend: Next.js + TypeScript.
- PostgreSQL é source of truth.
- NATS JetStream é a mensageria.
- Outbox Pattern para eventos confiáveis.
- Docker-first obrigatório.
- Docker Compose no primeiro estágio.
- Nginx como proxy público.
- OpenTelemetry.
- Tenant-first.
- RLS em tabelas tenant-owned.
- Hub BPO somente após core tenant.
- WhatsApp oficial como caminho principal, mantendo suporte arquitetural a provedores não oficiais opcionais.
- Omnira iOS Design System em todo frontend.

## Primeira ação do agente

Leia:

1. `README-AGENT.md`
2. `docs/REUSE-MAP.md`
3. `docs/ARCHITECTURE-COMPATIBILITY.md`
4. `docs/MIGRATION-WAVES.md`
5. `docs/LEGAL-ATTRIBUTION.md`
6. `PROMPT-CLAUDE-CODE.txt`

Depois audite o repositório upstream atual antes de copiar qualquer coisa.

## Regra principal

Não copiar implementação Next.js/Supabase para o backend OMNIRA.

O backend deve permanecer Go.

Aproveitar do Deskcomm principalmente:

- regras de domínio;
- edge cases;
- schema conceitual;
- testes/invariantes;
- contratos;
- componentes React;
- UX funcional;
- adapters como especificação;
- práticas de packaging;
- skills/harness.

## Source drift

O DeskcommCRM está evoluindo rapidamente.

Portanto:
- registrar o SHA do commit upstream usado em cada port;
- nunca escrever apenas "copiado do main";
- toda adaptação deve registrar `source_repo`, `source_path`, `source_commit`.

Ver `docs/SOURCE-TRACKING.md`.
