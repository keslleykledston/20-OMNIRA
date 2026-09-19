# Reuse Map — DeskcommCRM → OMNIRA

| Área | Classificação padrão | Ação |
|---|---|---|
| Tenant/RBAC/RLS | PORT + INSPIRE | portar invariantes e políticas para schema OMNIRA |
| Audit log | PORT | implementar equivalente Go/Postgres |
| API idempotency | PORT | aproveitar contratos e edge cases |
| Inbox frontend | ADAPT | reaproveitar React onde útil, trocar data layer |
| Contacts/Customer 360 | ADAPT + PORT | frontend adaptado; backend em Go |
| WhatsApp Meta Cloud | PORT | prioridade alta |
| WAHA / providers não oficiais | ADAPT + INSPIRE | permitidos como adapters opcionais, nunca como default do produto |
| ChannelAdapter abstraction | PORT | criar interface Go canônica |
| Routing/assignment | PORT | aproveitar regras, testes e state machine |
| Supervisor | ADAPT + PORT | frontend + métricas |
| Automation engine | PORT | schema/runtime em Go |
| Automation canvas | ADAPT | React Flow pode ser reaproveitado |
| Event log/workers | INSPIRE | manter NATS JetStream no OMNIRA |
| Docker packaging | INSPIRE + ADAPT | adotar boas práticas, manter Nginx |
| Skills/harness | ADAPT | incorporar padrões úteis |
| AI/RAG/MCP | INSPIRE | pós-MVP salvo necessidade |
| Nuvemshop | REJECT por ora | fora do MVP atual |
| Supabase Auth | REJECT | auth do OMNIRA permanece independente |
| Supabase Realtime | REJECT | realtime OMNIRA permanece próprio |
| Sentry foundation | REJECT | OTel é baseline |

## Prioridade

### P0
- Tenant/RLS test patterns
- Meta WhatsApp Cloud API
- Channel abstraction
- Inbox component boundaries
- Assignment/routing

### P1
- Contacts/Customer 360
- Supervisor
- Automation graph
- skills/harness

### P2
- LGPD tooling
- AI/RAG
- MCP patterns
- advanced CRM/pipeline
