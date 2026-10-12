# Hub — guia de migração e reversão

Estado: `LAB`. Ledger de produção: **121**. Receitas validadas nesta árvore.

## 1. Ordem e conteúdo

093–094 modelo e RLS · 095 leitura delegada · 096–098, 105 endurecimento (`search_path`, só `session_user`, empresa/usuário ativos) · 099–103 operadores, direitos, pré-autorização, gestão delegada · 104 pools · 106 índices de auditoria · 107–117 servir a empresa pelo Hub (contexto completo, contatos, ERP/chamados, transferir/notas, IA, finalizar, histórico) · 118 contexto de sessão assinado · 119 custo da política (`hub_reach`) · 120 `conversation.read_internal` (outra linha de trabalho) · 121 escopo das escritas de auditoria/outbox.

## 2. Aplicar em produção (sem atropelar outra sessão)

1. Backup do banco (`omnira_dev_<UTC>`), tags de volta das imagens: `docker tag 20-omnira-<svc>:latest 20-omnira-<svc>:rollback-pre-<tema>-<data>`.
2. Construir de `git archive HEAD` (nunca da árvore viva: pode ter edição de outra sessão).
3. Migrations: `docker compose run --rm --no-deps -v <dir do git archive>/migrations:/migrations:ro migrate`. **O serviço `migrate` monta o `./migrations` VIVO por padrão**; sem o `-v` ele aplicaria arquivos não commitados.
4. Subir: `docker compose up -d --no-deps --no-build --force-recreate api worker web` (**sempre `--no-deps`**).
5. Conferir: contêineres `healthy`, `scripts/hub-health-check.sh`, `omnira_session_ctx_enforce 1`, sondas de produção com contexto assinado, `docker exec omnira-web nginx -t`.

## 3. Voltar

- **Imagens:** `docker tag 20-omnira-<svc>:rollback-pre-… 20-omnira-<svc>:latest` e `up -d --no-deps --no-build --force-recreate <svc>`. Voltar só a imagem é seguro quando a migration nova é aditiva (a imagem antiga ignora o que não conhece); para desfazer a migration, volte primeiro as imagens e só então aplique o `.down.sql` correspondente, nunca o contrário.
- **Banco:** restaurar o backup escolhido num cluster novo é o caminho provado (`scripts/backup-restore-hub-check.sh`: RTO ~6 s no volume atual, isolamento conferido depois). Restaurar sobre o banco vivo só com o serviço parado e ordem do dono.

## 4. Provas que existem

`scripts/test-hub-migrations.sh` (up/down/up de 093 até a mais nova; PASS em 2026-10-12 com 121), `scripts/test-integration.sh` (gate de 37 pacotes, sempre de `git archive`, em `enforce`), `scripts/bench-hub-policies.sh` (custo a 10k/30k), `scripts/test-hub-load.sh` (16 clientes, 20k itens), `scripts/backup-restore-hub-check.sh`.

## 5. Armadilhas conhecidas

`docker compose up` sem `--no-deps` roda o `migrate` sobre o diretório vivo · `pkill -f <padrão que casa a própria linha>` mata o shell · editar Go enquanto o gate roda quebra o gate · `OMNIRA_ENV` em produção é `development` (não condicione comportamento a ele) · o Postgres descartável responde ao `pg_isready` do `initdb` antes de estar pronto.
