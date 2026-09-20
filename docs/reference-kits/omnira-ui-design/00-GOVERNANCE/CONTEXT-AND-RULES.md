# Contexto e regras obrigatórias

## Decisões congeladas

- Frontend: Next.js + TypeScript.
- Backend: Go.
- Tenancy: TenantContext explícito; o tenant ativo deve ser visível no shell.
- RBAC: Admin, Supervisor, Agente; Hub BPO depois.
- UI: um único Omnira iOS Design System em toda a aplicação.
- WhatsApp: multi-provider; Meta/BSP = oficial, WAHA/session-based = não oficial.
- Docker-first; Nginx público; nenhum secret no frontend.
- REST é fonte de re-sync; realtime atualiza a view.

## Prioridade visual

1. clareza operacional;
2. densidade moderada;
3. consistência;
4. acessibilidade;
5. responsividade;
6. fidelidade às referências;
7. performance.

## Não fazer

- não usar admin template genérico;
- não criar sidebar escura;
- não misturar bibliotecas de ícones;
- não colocar hex, radius e sombras espalhados pelas features;
- não implementar primitives duplicados dentro de telas;
- não transformar Next.js em backend principal;
- não mostrar tokens, chaves, sessão ou credenciais de providers;
- não comprimir três painéis do Inbox no mobile.

## Regra de ownership

- `packages/ui` (ou equivalente já existente): tokens, primitives e patterns genéricos.
- `features/<feature>`: componentes de negócio específicos.
- `app/.../page.tsx`: composição e routing, não lógica extensa.
- APIs/fixtures: adapter próprio por feature; trocar fixture por Go API sem reescrever visual.
