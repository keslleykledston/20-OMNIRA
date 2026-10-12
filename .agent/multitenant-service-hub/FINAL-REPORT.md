# Multi-Tenant Service Hub — relatório final (RASCUNHO, aguardando aceite do dono)

Estado do projeto: `LAB`. Este relatório só vira final com a assinatura do bloco 5 de `docs/architecture/HUB-GATE-PACKAGE.md`.

## Entregue (implantado em LAB, ledger 121, api/worker/web `sha-6d083a5`)
- Hub como plano de controle dentro do OMNIRA: contratos, concessões por pessoa, painel de acessos, convite por e-mail, pools e distribuição automática, auditoria.
- Atender uma empresa pelo Hub (ADR-0040): caixa completa com contatos, ERP/chamados, transferir/notas, IA de apoio, finalizar, histórico — cada capacidade atrás de uma chave delegável.
- Isolamento em profundidade: o servidor resolve Hub → contrato → concessão; RLS FORCE com `omnira_app` não-superusuário; contexto de sessão assinado em `enforce` (ADR-0042); escrita de auditoria/outbox restrita (121); uma instância por vez; empresa suspensa não é servida.
- Escala e operação: `hub_reach()` (lista 30k de 5,6 s para 0,28 s), sinal de mudança em tempo real (SSE sem conteúdo), métricas, `hub-health-check` em cron, restauração de backup provada.
- Workspace no layout aprovado (fila × histórico, abas, painel recolhível) sem copiar nada do protótipo do Lovable.

## Provas
Ver o bloco 1 do pacote do gate. Resumo: integração 37/37, Vitest 916/916, E2E real 14/14 (+ varredura de vazamento), Keycloak 6/6, migrations up/down/up, carga, restauração, 3 camadas do Codex com CRITICAL 0.

## Não construído (decisão de produto do dono)
Base de conhecimento por tenant para a IA; painel de supervisão do Hub; pontuação/afinidade de roteamento; faturas, 2ª via e abrir OS no ERP (dependem da API do ERP); alertas OPEN/ACK; busca global ⌘K; visões salvas; logo da empresa.

## Não provado
Seção 3 do pacote do gate.

## Como continuar
1. Dono: itens da seção 3 e a decisão da seção 5.
2. Piloto supervisionado com `docs/ops/HUB-LIVE-TEST-SCRIPT.md`.
3. Só então rever o estado do projeto; nenhuma tag até lá.
