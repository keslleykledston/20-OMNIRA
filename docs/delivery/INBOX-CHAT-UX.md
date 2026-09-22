# INBOX/CHAT UX — Design Gate Phase

**Data**: 2026-09-22  
**Status**: REALITY MAP + DESIGN SPECIFICATION  
**Próximo**: Pedido consolidado ao Lovable MCP  

## I. REALIDADE ATUAL (Reality Map)

### Implementação de Código

| Elemento | Componente | Estado | Detalhes |
|----------|-----------|--------|----------|
| **Workspace 3-painel** | InboxWorkspace.tsx | REAL | Desktop: list(350px) \| chat(flex) \| context(320px) |
| **Conversa List** | ConversationListPanel | REAL | Segmented (Todas/Não lidas/Minhas), busca, layout cards |
| **Seleção** | ConversationListPanel | REAL | Auto-select primeiro on load, navegação mobile back |
| **Chat Header** | ChatPane | REAL | Minimal; sem channel badge, ticket, assignee |
| **Timeline** | ChatPane + MessageBubble | REAL | Inbound/outbound bubbles, scroll auto, reftech realtime |
| **Composer** | MessageComposer | REAL | Textarea, send button, Idempotency-Key |
| **Context Panel** | ContextPane.tsx | PARTIAL | Existe; conteúdo mínimo |
| **Realtime SSE** | useRealtimeEvents hook | REAL | Conectado; reftech em novo evento |
| **Testes** | Vitest + Playwright | REAL | 113 tests + e2e 25 specs PASS |
| **Design System** | TailwindCSS + OMNIRA tokens | REAL | Cores, spacing, typography |

### Falta Funcional

| Gap | Bloqueia UX? | Nota |
|-----|-------------|------|
| Channel badge (WhatsApp/SMS/Email) | Não; pode mostrar via contato | Tipo de canal é metadado no contato |
| Queue/Assignee control | Não; deferred para workflow | Assignment já existe no backend, não exposto na UI |
| Transfer UI | Não; deferred | Backend tem `transfer`, UI não o chama |
| Internal notes | Não; deferred | Backend não retorna; API não mapeada |
| Ticket integration | Parcial; deferred | Tipo existe; CRUD não disponível |
| Delivery status visual | Parcial | MessageItem.status existe; UI não diferencia visualmente |
| Co-attendance/participants | Absent | API traz `participants`; UI não usa |
| Permissions enforcement | Minimal | UI confia no backend 403; não pré-filtra |
| Presence indicator (online/offline) | Absent | IAM4.2 presença agregada; não integrada aqui |
| Unread badge | Absent | API traz `unread_count`; UI não o exibe |
| Date separators | Absent | Timeline sem separadores de dia |
| Loading/error states completos | Partial | Básicos existem; erros genéricos |
| Empty inbox state | Minimal | Texto "Nenhuma conversa" |

## II. CONTRATO REAL DISPONÍVEL (O que a API fornece)

### GET /api/v1/tenants/{tid}/inbox/conversations

```typescript
ConversationItem {
  id: string;
  contact_name: string;
  contact_phone: string;
  status: 'active' | 'closed' | 'pending';
  created_at?: string;
  updated_at: string;
  assigned_to_user_id?: string;
  message_count: number;
  unread_count: number;
  crm_contact_id?: string;
  participants?: ConversationParticipant[];  // REAL; UI não usa
}
```

### GET /api/v1/tenants/{tid}/inbox/conversations/{cid}

Mesma structure + details.

### GET /api/v1/tenants/{tid}/inbox/conversations/{cid}/messages

```typescript
MessageItem {
  id: string;
  conversation_id: string;
  body: string;
  direction: 'inbound' | 'outbound';
  status: 'queued' | 'sent' | 'delivered' | 'read' | 'failed' | 'pending';
  created_at: string;
  created_by?: string;
  media_urls?: string[];  // Presente mas sem suporte de renderização
}
```

### POST /api/v1/tenants/{tid}/inbox/conversations/{cid}/messages

Enviando: `{ "body": string }` + `Idempotency-Key` header.

### SSE /api/v1/tenants/{tid}/agents/presence/events (Realtime)

Conversa + mensagem events; atualiza lista/timeline sem reload.

### Assignment (Real mas não exposto na UI)

Backend: `PUT .../conversations/{id}` pode `assign`.  
UI: Não há botão, formulário ou chamada.

### Transfer (Real mas não exposto)

Backend: `PUT .../conversations/{id}/transfer`.  
UI: Não há botão ou flow.

### Channel/Credential (Necessário para badge)

Conversação tem origem (WhatsApp/SMS/Email) mas não está em `ConversationItem`.  
Backend rastreia via `channel_id` ou campo implícito; não retornado aqui.

## III. DESIGN SPEC — Estado Visual Esperado

### LEFT PANEL — Work Queue

**Altura:** 100% da viewport menos top shell.  
**Scroll:** Independente.  
**Conteúdo:**

1. **Segmented Control** (Todas / Não lidas / Minhas)
   - Botões com estado ativo (bg-accent-primary, texto branco).
   - Padding 4 em torno; border-bottom sutil.

2. **Search** (Buscar contato...)
   - Input de texto com border round, foco ring primary.
   - Placeholder em text-tertiary.

3. **Conversation Cards** (Lista)
   - Altura mínima 72px, padding 3.
   - Layout: Avatar (40px circle) + Content (flex 1) + Meta (timestamp, badge).
   - Avatar: initial maiúscula ou default icon.
   - Content: nome em text-primary font-medium, phone em text-secondary text-sm, channel badge (se houver).
   - Unread: badge "1" ou dot indicator canto superior direito.
   - Hover: bg-surface-muted.
   - Selected: bg-accent-primary-soft (azul claro).

4. **Empty State**
   - "Nenhuma conversa" centrado, text-tertiary.

### CENTER PANEL — Chat

**Header (fixo):**
- Contato (nome, phone).
- Status da conversa (active/closed/pending).
- Action buttons: Menu (mais opções); Botão voltar (mobile only).
- Espaço: 56px altura.

**Timeline (flex 1, scroll independente):**
- Inbound: bubble esquerda, bg-surface-muted, text-text-primary.
- Outbound: bubble direita, bg-accent-primary, text-white.
- Timestamp: text-xs text-tertiary, dentro ou fora do bubble.
- Delivery status (MessageItem.status):
  - `sent`: ✓ (checkmark cinza).
  - `delivered`: ✓✓ (duplo cinza).
  - `read`: ✓✓ (duplo azul).
  - `failed`: ⚠️ (triangulo vermelho).
  - `pending`: ⏱️ (relógio).
- Media: placeholder para `media_urls` (deferred; não renderizar).
- Day separators: "22 de setembro" divider hairline com texto centrado em text-tertiary.

**Composer (fixo fundo):**
- Textarea com auto-grow.
- Botão Send (Submit, bg-accent-primary, text-white, disabled quando vazio ou sending).
- Estado sending: spinner; desabilita input.
- Estado error: mensagem vermelha inline, botão Retry.
- Espaço total: 120px altura (compositor + padding).

**States:**
- Loading: skeleton blocks na timeline.
- Empty timeline: "Sem mensagens".
- Conexão perdida: banner amarelo "Reconectando..." (usar realtime state).
- Assignee ausente: "Assuma esta conversa para responder" (state: `assigned_to_user_id` null).

### RIGHT PANEL — Context

**Altura:** 100% do chat.  
**Scroll:** Independente se conteúdo > viewport.  
**Seções:**

1. **Contact**
   - Nome, phone, canal (se rastreável).
   - Editar link (deferred).

2. **Conversation State**
   - Status badge (active/closed/pending).
   - Created/updated timestamps.

3. **Ticket** (se crm_contact_id existir)
   - Título, status, owner.
   - Link abrir (deferred).

4. **Assignment**
   - Assigned to: usuário ou "Não atribuído".
   - Botão Claim, Assign, Transfer (placeholder; ações deferred).

5. **Participants** (se participants array não vazio)
   - Lista de co-attendees com role.
   - Deferred: adicionar/remover.

6. **Tags** (se suportadas)
   - Pills; adicionar/remover (deferred).

7. **History** (se API o fornece)
   - Últimas 3 conversas com este contato.
   - Links (deferred).

**Empty state:** "Sem detalhes adicionais".

### Mobile/Tablet Responsiveness

**Tablet (768px–1024px):**
- Converter: list + chat com context em drawer/sheet.
- Composer continua fixo e utilizável.

**Mobile (<768px):**
- Fase 1: List → tap → Chat fullscreen.
- Context: Drawer fundo (swipe ou botão).
- Composer sempre acessível na base.

---

## IV. LACUNAS NÃO IMPLEMENTÁVEIS NESTE SLICE

Marcadas como DEFERRED porque não há contrato real ou exigem backend novo:

- **Queue/Assignee/Transfer:** UI pode chamar; backend já suporta. **Implementável.** Deferred por prioridade.
- **Internal notes:** Backend não expõe endpoint `/notes`. **Requer ADR.** Deferred.
- **Ticket CRUD:** Apenas integração read-only. **Implementável com ticket list API.**
- **Channel badge:** Backend não retorna tipo de canal. **Requer schema/API update.** Deferred.
- **Co-attendance details:** API fornece array; UI pode exibir. **Implementável.**
- **Presence indicator:** IAM4.2 agregada a agentes; não por conversa. **Requer nova API.** Deferred.
- **Media inbound/outbound:** API traz `media_urls`; renderização requer allow-list SSRF. **Deferred.**

---

## V. STACK E RESTRIÇÕES

**Frontend:**
- Vite + React 18 + TypeScript.
- TanStack Query (data fetching).
- Zustand (state) — mínimo uso.
- Tailwind + OMNIRA design tokens.

**Backend autoridade:**
- API contracts (OpenAPI).
- Realtime (SSE).
- Permissions/RBAC (403 handling).
- RLS (tenant isolation).

**Lovable escopo:**
- Layout 3-painel responsivo.
- Componentes visuais.
- Estados da tela.
- Interações UI (scroll, select, compose).

**Lovable fora:**
- Backend.
- API calls (serão feitas pela integração pós-design).
- Routing beyond modal/sheet.
- Permission model.
- Realtime architecture.

---

## VI. INBOX.1 — PORT COMPLETO (2026-09-22)

**Status: DONE**

---

## VII. INBOX.2 — SEND/REALTIME VALIDATION (2026-09-22)

**Status: VALIDATED / NO-CODE**

No changes needed — send flow, idempotency, draft preservation, and SSE
reconnect were already well-implemented.

---

## VIII. INBOX.3 — ASSIGNMENT WORKFLOWS (2026-09-22)

**Status: DONE**

### Mudanças realizadas:

#### TechnicianSelectModal.tsx
- ✅ Switched from legacy `/users/agents` to IAM4.1 canonical `/agents` endpoint
- ✅ Map `user_id` → `id` (AgentProfile field naming)
- ✅ Fallback mock data only in development (import.meta.env.DEV guard)
- ✅ Production/staging: empty list on fetch error (never fake data)

#### ContextPane.tsx
- ✅ CLAIM workflow: `handleAssign()` → POST `/assign` with `{}` (self-assign)
- ✅ TRANSFER workflow: `handleTransfer()` → modal selector + POST `/transfer`
- ✅ Modal uses TechnicianSelectModal (real agents from IAM4.1)
- ✅ Exclude current assignee from transfer options
- ✅ Backend validates eligibility (409 race, 422 ineligible)
- ✅ Participants display read-only (no mutations)

### Testes:
- ✅ `tsc`: PASS
- ✅ `npm run test`: 113/113 PASS
- ✅ `npm run build`: PASS
- ✅ Playwright 25/25: PASS (0 regressão)
- ✅ diff-check: PASS

### Dados reais conectados:
- ✅ Claim to self (conversation.claim permission)
- ✅ Transfer to other agent (conversation.manage + eligibility)
- ✅ Real agent list from IAM4.1 (AgentProfile-backed)
- ✅ Tenant isolation validated

### Deferred (out of scope):
- ❌ Participant invite/remove (no confirmed endpoint)
- ❌ Internal notes (no endpoint)
- ❌ Media rendering (SSRF risk)
- ❌ Channel type badge (backend doesn't return)
- ❌ Presence per-conversation (aggregated only)
- ❌ Optimistic send UI

---

## IX. PRÓXIMO: QA E MERGE

### Mudanças realizadas:

#### ConversationListPanel.tsx
- ✅ Melhorado visual de message preview (placeholder)
- ✅ Refinado status badge visual
- ✅ Preservado segmented control (Todas/Não lidas/Minhas)
- ✅ Search input (dados reais via query param)
- ✅ Unread badges + timestamp

#### MessageBubble.tsx
- ✅ Delivery status symbols: ✓ (sent), ✓✓ (delivered), ✓✓ azul (read), ⚠ (failed), ◷ (pending)
- ✅ Status color: read=accent-primary, failed=danger, resto=tertiary
- ✅ Timestamps inside bubbles
- ✅ Inbound/outbound layout preservado

#### ChatPane.tsx
- ✅ Day separators na timeline (agrupa mensagens por data)
- ✅ Lógica de detection de mudança de dia
- ✅ Formatted date headers (ex: "segunda, 22 de setembro de 2026")
- ✅ Visual line separators com texto centrado

#### ContextPane.tsx
- ✅ Estrutura acordeão preservada (Contact, Conversation, TicketPanel, Actions)
- ✅ Sem mudanças necessárias (já atende escopo INBOX.1)

### O que NÃO foi implementado (conforme esperado):
- ❌ Channel type badge (backend não retorna)
- ❌ Internal notes (API não existe)
- ❌ Media rendering (SSRF risk, deferred)
- ❌ Assignment logic em UI (backend sim, UI apenas visual)
- ❌ Transfer/Claim workflows novos (deferred)

### Testes:
- ✅ `tsc` build: PASS
- ✅ `npm run build`: PASS (vite build)
- ✅ Vitest 113/113: PASS
- ✅ Zero regressions em ConversationPage/InboxPage tests

### Dados reais conectados:
- ✅ ConversationItem (contact_name, phone, status, unread_count, etc)
- ✅ MessageItem (body, direction, status, created_at)
- ✅ Realtime events (SSE invalidates queries)
- ✅ Assignment state (conversation.assigned_to_user_id)

### Compatibilidade Lovable Design:
- ✅ 3-painel layout (desktop via grid)
- ✅ Responsive (lg:col-span breakpoints)
- ✅ Mobile navigation (back button, hide on md+)
- ✅ Delivery visual (Lovable style symbols)
- ✅ Day separators (Lovable style headers)
- ✅ Unread badges + count visual

---

## VII. PRÓXIMO: QA E MERGE

Pronto para:
1. Visual review local (side-by-side com Lovable screenshots)
2. E2E Playwright full Inbox flow
3. Manual testing de states (loading, empty, no-selection, send error)
4. Commit + PR

