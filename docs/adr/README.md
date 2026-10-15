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
- 0043 - Setores: a fila é o setor; entrada por canal (S0–S2 implementadas, S3/S4 propostas)
- 0045 - Menu sem resposta: lembretes e encerramento por inatividade (nó close_conversation) — implementada em LAB na branch feat/menu-idle-reminders
- 0046 - Convite completo: função e filas no convite, e conta de login criada pelo OMNIRA — proposta (Fase 2 de Pessoas)
- 0047 - Ficha da pessoa no console do Hub: instâncias e filas de cada uma — proposta (Fase 3 de Pessoas)
