# Headless — benchmark e consumo (30 000 conversas)

Medição de **regressão e de ordem de grandeza**, não promessa de SLA. Ambiente: um servidor de desenvolvimento, PostgreSQL 16 (Docker, configuração padrão, sem tuning), papel `omnira_app` (RLS avaliado, FORCE), NATS descartável; cliente e servidor na mesma máquina. Reexecutar no ambiente de destino antes de qualquer decisão de capacidade.
Como repetir: `OMNIRA_BENCH_CONVERSATIONS=30000 bash scripts/test-integration.sh ./internal/external/adapters` com `-run TestBenchmarkHeadlessAtScale` (teste em `internal/external/adapters/h6_benchmark_integration_test.go`; sem a variável ele é ignorado).

## Carga
1 empresa, **30 000 conversas**, 90 000 mensagens, a instalação já existente (os produtores do journal ligados, como em produção) → **120 005 fatos** escritos nas mesmas transações. 800 pares claim+send com 8 clientes em paralelo.

## Resultados (última execução)
| Medida | Resultado |
|---|---|
| Inserção da carga (30k conversas + 90k mensagens + 120k fatos) | 21,6 s |
| Sequenciador: numerar 120 005 fatos | 1,4 s (~85 000 fatos/s) |
| Lista de conversas, 1ª página (100 itens) | 16 ms |
| Lista, páginas por cursor (150 páginas) | p50 17 ms · p95 19 ms |
| Uma conversa por id | p50 3,8 ms · p95 5,8 ms |
| Mensagens de uma conversa | p95 13 ms |
| Ler o journal inteiro em páginas de 200 (120 005 fatos) | 14,2 s (~8 400 fatos/s) |
| Comandos: claim + envio (par), 8 em paralelo | 133 pares/s · p95 61 ms |
| Fan-out de webhooks: 121 605 entregas materializadas para 1 assinatura | 3,8 s (~32 000/s) |
| **Deadlocks** em toda a execução | **0** |

Lag do sequenciador = tempo para numerar o atraso que a carga gerou (1,4 s para 120 mil fatos). Em operação normal o laço roda a cada 500 ms e o atraso é o de uma transação longa (alerta em 60 s, ver runbook).

## O que a medição achou (e o que foi feito)
1. **Lista sem índice por `created_at`: ~1,4 s por página** com 30 000 conversas (o banco ordenava todas as conversas visíveis e avaliava as políticas de RLS em cada uma). Com `idx_conversations_tenant_created` (migration 139) a página caiu para ~17 ms. A ordenação por `updated_at` já tinha índice desde a migration 14.
2. **Estatísticas velhas no journal**: depois de numerar 120 mil fatos de uma vez sem `ANALYZE`, uma página do journal passou de 2 minutos (plano ruim, o planejador achava que nenhum fato tinha número). Em produção o autovacuum corrige, mas o journal é reescrito e aparado em bloco: as migrations 137/138 passam a analisar cedo (`autovacuum_analyze_scale_factor = 0,02`) em `domain_events` e `webhook_deliveries`. O benchmark roda `ANALYZE` após a carga, como o autovacuum faria.
3. **Sem deadlock** sob 8 clientes + sequenciador + fan-out; a matriz de corridas (testes de H2 a H5) cobre ordens de lock com repetição.

## Limites desta medição
Uma empresa, uma instalação, um assinante; sem latência de rede; sem tráfego simultâneo do Workspace; sem Meta/WAHA reais (o envio é medido até o job de entrega, não até o provedor). Entrega real de webhooks (HTTP) não foi medida em volume, só o fan-out; o worker serve assinaturas em paralelo (padrão 8) e uma assinatura lenta não atrasa as demais (teste de concorrência).
