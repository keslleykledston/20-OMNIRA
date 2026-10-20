# ADR-0051: Superfície de leitura de grupos do WhatsApp na API externa (Headless)

## Status
**Proposto (2026-10-20).** Reabre G6 de `docs/ops/HEADLESS-CHATWOOT-MAPPING.md` §4, a pedido explícito do dono do produto nesta sessão. G6 estava **fechada em 2026-10-18** ("o CRM usa só contatos hoje; grupos ficam para o futuro"), mas o próprio texto da decisão já previa o caminho: *"Se o CRM precisar deles, é uma superfície nova e só de leitura (`/external/groups`), com ADR"* — este ADR é exatamente esse caminho, não uma contradição da decisão anterior.

## Contexto
O pacote `OMNIRA_API_PENDENCIAS_CRM.md` pede, como item P1: `kind`/`is_group`/`jid` em contato/conversa, endpoint de participantes, e remetente (`name`/`phone_e164`/`jid`) em mensagens inbound de grupo.

Por ADR-0015 (aceita, implementada), grupos do WhatsApp **não são** `contacts`/`conversations`/`messages` — vivem em `wa_groups`/`wa_group_messages`, modelo próprio, somente leitura, com `group.read`/`group.manage` restritos a admin/supervisor, opt-in por grupo (desligado por padrão), RLS forçado e retenção quente/fria já implementadas. A API externa (`internal/external`) hoje não tem nenhuma referência a `wa_group*` — grupos **nunca aparecem** para uma aplicação externa, por design.

Confirmado por inspeção nesta sessão: `ContactDTO.kind` existe mas com valores `unclassified|customer|other` (classificação de contato humano, ADR-0018), não "é grupo"; `SenderDTO` não carrega nome/telefone/jid; não há rota de participantes em lugar nenhum.

## Decisão
1. **Não misturar grupo em `contacts`/`conversations`/`messages` da API externa** — mantém a separação de ADR-0015; evita reabrir a decisão original do modelo 1:1, que já foi cara (`exceto se for grupo` espalhado por roteamento/fila/ticket/SLA).
2. **Nova superfície só de leitura**, espelhando o que o Workspace já tem para grupos (G1-G4 de ADR-0015), sob a API external:
   - `GET /api/v1/external/groups` — grupos **habilitados** visíveis à instalação (mesma allowlist de canal da instalação, nunca de outro tenant), com prévia da última mensagem;
   - `GET /api/v1/external/groups/{id}/messages` — histórico paginado por cursor, mesmo padrão de `conversations/{id}/messages`;
   - Autor de cada mensagem: **nome apenas** (`author_name` sanitizado), nunca telefone nem JID real do participante — ADR-0015 já decidiu minimizar isso ("o número só aparece onde for estritamente necessário"); este ADR não reabre essa parte.
3. **Novo scope** `group.read.external` (nome a confirmar na implementação), separado de `group.read` (que é para usuários do Workspace) — mínimo privilégio, só leitura, nunca concede `group.manage` (habilitar/desabilitar/apagar histórico continua exclusivo da tela do Workspace).
4. **Sem escrita**: nenhuma aplicação externa pode habilitar grupo, enviar mensagem a grupo ou apagar histórico — isso permanece fora de escopo (ADR-0015 já reserva envio a grupo para ADR próprio, este não é esse ADR).
5. **Participantes**: expor lista de participantes (nome, sem telefone) **somente se** a instalação tiver `group.read.external` e o grupo estiver habilitado; caso contrário, 404 igual a um grupo inexistente (sem oráculo de enumeração entre tenants, mesmo padrão de ADR-0015).
6. **Tenancy**: testes obrigatórios Tenant A/B contra PostgreSQL real, igual a todo o resto — grupo de um tenant nunca aparece para instalação de outro, credencial revogada perde acesso imediatamente.

## Consequências
- Ganho: CRM que precisar de grupos (hoje nenhum caso concreto registrado) tem caminho, sem contaminar o modelo 1:1 nem o contrato de `conversations`.
- Custo: mais um par de endpoints, mais um scope, mais OpenAPI/AsyncAPI a manter; grupos continuam exigindo opt-in explícito por administrador do Workspace (este ADR não muda isso).
- Nenhuma migration nova esperada — `wa_groups`/`wa_group_messages` já existem; a mudança é só de camada de leitura/autorização para expor o que já existe ao mundo externo.

## Alternativas
- **Não implementar agora** (manter G6 fechada como está): válido se nenhum cliente do CRM pedir grupos de fato — **recomendado até haver um pedido concreto**, já que o documento de pendências do CRM cita isso como item P1 "de dia a dia", não bloqueante, e não há caso de uso descrito (qual fluxo do CRM precisa de grupo?).
- **Reusar `group.read` do Workspace como scope da API externa**: mistura autorização de usuário humano com autorização de aplicação, modelo diferente do resto da API external (que sempre tem scope próprio por capability). Rejeitada por consistência.

## Questões em aberto
1. Qual é o caso de uso concreto do CRM para grupos? O documento de pendências não descreve um fluxo — vale confirmar com o CRM antes de implementar, para não construir superfície sem consumidor real.
2. Nome final do scope (`group.read.external` é só uma proposta de trabalho).
