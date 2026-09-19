# Frontend UX — OMNIRA

## Objetivo

Criar uma interface operacional simples, elegante e rápida, inspirada nos princípios de usabilidade do iOS:

- hierarquia visual clara;
- ações previsíveis;
- feedback imediato;
- navegação curta;
- espaços generosos;
- redução de ruído;
- controles consistentes;
- foco na tarefa atual.

Não copiar componentes, ícones ou layouts proprietários da Apple literalmente.

## Stack

- Next.js + TypeScript.
- Design system próprio do OMNIRA.
- CSS variables/tokens.
- componentes acessíveis.
- rendering/client state conforme necessidade, sem SPA complexa por padrão.

## Princípios visuais

### Tipografia
Usar system font stack moderna:

```css
font-family:
  Inter,
  ui-sans-serif,
  system-ui,
  -apple-system,
  BlinkMacSystemFont,
  "Segoe UI",
  sans-serif;
```

### Superfícies
- fundo neutro claro no tema light;
- dark mode futuro preparado por tokens;
- cards com contraste sutil;
- cantos arredondados moderados;
- shadows leves;
- blur/translucência apenas onde ajudar hierarquia.

### Espaçamento
Base de 4px/8px.

### Motion
- transições curtas;
- sem animações decorativas longas;
- respeitar `prefers-reduced-motion`.

## Navegação desktop

```text
┌──────────────────────────────────────────────┐
│ Sidebar │ Header / Context                   │
│         ├────────────────────────────────────│
│         │ Workspace                          │
│         │                                    │
└──────────────────────────────────────────────┘
```

Sidebar:
- Inbox;
- Contatos;
- Tickets;
- Supervisor;
- Automação;
- Integrações;
- Configurações.

Itens indisponíveis no release atual podem ser ocultados, não simulados.

## Mobile/responsive

Usar navegação inferior apenas para as funções essenciais.

Exemplo futuro:
- Inbox;
- Tickets;
- Contatos;
- Mais.

## Tenant Context

A identidade do Tenant ativo deve ser óbvia.

Mostrar consistentemente:
- nome;
- avatar/logo;
- badge/contexto.

Quando Hub entrar:
- troca de Tenant nunca pode parecer mudança invisível;
- limpar seleção sensível ao trocar contexto;
- cores podem ajudar orientação, mas nunca serem o único indicador.

## R0.1 — frontend mínimo

Páginas:

```text
/login
/app
/app/tenants
/app/tenants/{id}
/app/tenants/{id}/members
/app/tenants/{id}/audit
```

### Dashboard inicial
Cards simples:
- Tenant atual;
- membros;
- status de serviços;
- últimos eventos de auditoria.

## R0.2 — shell de atendimento

Desktop:

```text
Queues/Tickets | Conversation | Customer Context
```

Em viewport menor:
- uma coluna por vez;
- navegação back previsível;
- composer fixo;
- actions em bottom sheet/dialog.

## Estados obrigatórios

Todo componente de dados deve possuir:
- loading;
- empty;
- error;
- success/ready;
- permission denied quando aplicável.

## Acessibilidade

- navegação por teclado;
- focus visível;
- contraste mínimo WCAG AA;
- labels sem depender de placeholder;
- touch targets adequados;
- sem cor como único sinal.

## Performance UX

- skeleton somente quando faz sentido;
- optimistic UI apenas em operações seguras;
- ações externas (ERP/canal) mostram status real;
- nunca exibir sucesso antes de confirmação quando a operação não for reversível.
