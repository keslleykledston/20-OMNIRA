# ADRs

ADRs registram decisões:
- difíceis/caras de reverter;
- surpreendentes sem contexto;
- resultantes de trade-off real.

Formato:
- Status
- Contexto
- Decisão
- Consequências
- Alternativas

Não criar ADR para escolha trivial ou facilmente reversível.

## Índice recente
- 0017 - Conversation, TopicThread e Ticket são conceitos distintos (Conversation Intelligence)
- 0018 - Usuários internos × contatos externos × contas de cliente
- 0019 - Visual Flow Builder, Flow Runtime e biblioteca de Templates/Packs
- 0020 - Finalizar atendimento e memória do contato entre atendimentos
- 0021 - Android e iOS como novos clientes do OMNIRA Core (direção, segurança do app, offline, observabilidade)
- 0022 - Autenticação Web (cookie de sessão) × Mobile (credencial por aparelho) — aceita (2026-10-07); implementação no MOBILE.1
- 0023 - Contrato de eventos em tempo real e arquitetura de notificações
- 0024 - Anexos de saída (o operador envia arquivo ao cliente) — aceita, implementada, desligada até o teste ao vivo
- 0036 - Estratégia de evolução do frontend: o OMNIRA é a aplicação, protótipos gerados são só referência de UX — aceita
- 0043 - Setores: a fila é o setor; entrada por canal (S0–S2 implementadas; S3 visibilidade por fila implementada atrás de `sector_scope`, desligada por padrão; S4: transferência entre filas e atributos do setor)
- 0045 - Menu sem resposta: lembretes e encerramento por inatividade (nó close_conversation) — implementada em LAB na branch feat/menu-idle-reminders
- 0046 - Convite completo: função e filas no convite, e conta de login criada pelo OMNIRA — implementada (Fase 2 de Pessoas)
- 0047 - Ficha da pessoa no console do Hub: instâncias e filas de cada uma — implementada, versão mínima só no frontend (Fase 3 de Pessoas)
- 0048 - OMNIRA Headless / Híbrido: CRM externo e Workspace sobre um único controle de conversa (identidade de aplicação, journal durável, webhooks) — implementação local na branch feat/headless-crm-dual-operation
- 0049 - Service Context & Operations: protocolo de atendimento, linha do tempo, ocorrências, tarefas e registros externos — proposto, em implementação
- 0050 - Abrir conversa outbound por telefone na API externa (Headless) — aceito e implementado (questão do número do Gateway segue em aberto, mas como follow-up operacional não bloqueante)
- 0051 - Superfície de leitura de grupos do WhatsApp na API externa (Headless) — aceito e implementado (caso de uso: pedido P1 do CRM)
- 0052 - Listar templates aprovados na API externa (`template.read`) — aceito e implementado
- 0053 - Nota interna na API externa como mensagem `private` (paridade com Chatwoot) — aceito e implementado; revisão do Codex achou e corrigiu vazamento de visibilidade no Workspace/Hub
- 0054 - Platform Release/Update Engine: CI/CD, catálogo assinado, Update Manager e Update Agent para a instalação SaaS inteira (fases F0–F11) — aceito, em implementação na branch `feat/omnira-release-update-engine`
