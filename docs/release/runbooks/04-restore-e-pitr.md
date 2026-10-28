# Restore, PITR e limites reais

Cobre: o que fazer diante de um `RECOVERY_REQUIRED` (classe R3 — dado incompatível/corrompido), e os limites **reais** (não o alvo aspiracional) de RPO/RTO desta instalação. Política normativa: `release/policies/backup.yaml`.

## 0. O que este host tem de verdade (não confundir com o alvo de `docs/operations/DISASTER-RECOVERY.md`)

| | Estado real | Alvo documentado em outro lugar |
|---|---|---|
| Mecanismo | `pg_dump -Fc` lógico (`scripts/backup-omnira-db.sh`) | PITR/base backup contínuo |
| PITR disponível | **não** (`pitr_available: false`) | Postgres gerenciado Multi-AZ com WAL archiving |
| Agendamento (cron) | **não instalado neste host** (`crontab -l` vazio, achado F0 §5.3) | execução periódica automática |
| RPO real | tempo desde o **último `pg_dump` bem-sucedido** | ~5 min (só alcançável com PITR) |

**RPO real = tempo desde o último dump bem-sucedido, nunca "quase zero".** Prometer um RPO menor que isso seria inventar proteção inexistente — proibido pela especificação §4. Enquanto não houver um ADR adotando Postgres gerenciado com WAL archiving, `release/policies/backup.yaml` nunca declara um RPO "stable" menor que `min_rpo_minutes: 60`.

## 1. Classes de recuperação e o que cada uma exige de evidência de backup

| Classe | Descrição | Evidência exigida |
|---|---|---|
| R0 | abortar antes de qualquer mutação | nenhuma |
| R1 | código novo falhou, schema compatível — reimplanta digest antigo | nenhuma (não toca dados) |
| R2 | migração expand compatível + falha pós-upgrade — mantém schema, reimplanta código antigo | ledger de migrations consistente |
| **R3** | **dado incompatível/corrompido — `RECOVERY_REQUIRED`, restore manual aprovado** | **restore verificado em instância descartável** |
| R4 | release revogada/comprometida — isola, nunca downgrade cego | nenhuma (trata assinatura/revogação, ver `07-revogacao.md`) |

## 2. Diante de um `RECOVERY_REQUIRED` (R3)

1. **Preserve evidências primeiro** — não tente nada destrutivo antes de capturar o estado:
   ```sh
   /opt/omnira/update-agent/bin/omnira-updatectl status --installation-id=<id> --run=<run_id>
   ```
   Isso lista o journal completo de checkpoints (o que falhou, quando, com qual detalhe).
2. **Confirme o escopo real do dano** — qual tabela/dado está incompatível. O Update Agent nunca promete rollback automático de dados (`updatectl rollback` só reverte código/config via Compose — ver `03-rollback-e-recovery.md`); `*.down.sql`/`make migrate-down` **nunca** é o caminho para isto (down.sql não autoriza rollback de dados).
3. **Autoteste do mecanismo de restore** (fixtures próprias, qualquer momento — prova que o próprio mecanismo de dump/restore/RLS/isolamento funciona, **nunca** uma prova sobre o dump real que será restaurado):
   ```sh
   PG_CONTAINER=omnira-postgres scripts/backup-restore-check.sh
   ```
   Builda um banco descartável, aplica as migrations reais, faz `pg_dump -Fc`, restaura em (a) um banco novo do mesmo cluster e (b) um cluster Postgres **totalmente novo** (roles restauradas via `pg_dumpall --roles-only`), e compara checksums de linha das tabelas críticas, o ledger de migrations, as flags/policies de RLS e isolamento de tenant comportamental (`omnira_app`). Nada fora dos bancos/containers descartáveis é tocado.
4. **Valide o dump REAL que será restaurado, antes de tocar produção** — o passo 3 nunca substitui isto, porque roda contra fixtures, não contra o dump real do incidente. **Fixe explicitamente qual dump e qual container ao vivo** (sem variáveis, o script usa `DUMP=$(ls -t backups/omnira_dev/omnira_dev_*.dump | head -1)` — o mais recente — e `LIVE=omnira-postgres`, nenhum dos dois necessariamente certo para ESTE incidente, ex. um restore que precisa do dump anterior à corrupção):
   ```sh
   DUMP=backups/omnira_dev/<arquivo exato escolhido para este incidente>.dump \
   LIVE=omnira-postgres PG_OWNER=omnira DB_NAME=omnira_dev \
   scripts/backup-restore-hub-check.sh
   ```
   Restaura **esse** dump num cluster descartável e prova as mesmas garantias (integridade/RLS/isolamento, agora também o conjunto Hub) contra o dado real que será de fato usado na recuperação. **Só prossiga para o passo 5 se este passo, com o `DUMP` exato que será restaurado, tiver sido confirmado com sucesso** — um dump corrompido/inadequado, ou o dump errado (ex. o mais recente, quando o incidente exige um anterior), nunca deve passar pelo pré-requisito de recuperação só porque o autoteste de fixtures (passo 3) passou.

   **Atenção ao restaurar um dump mais antigo (ex. anterior à corrupção, não o mais recente):** duas das checagens do script comparam o restaurado contra o estado ATUAL — ledger de migrations igual à migration mais nova do checkout, e contagem de linhas das tabelas de controle do Hub igual ao banco vivo no momento da checagem. Um dump legitimamente mais antigo vai reprovar essas duas checagens por motivos esperados (menos migrations aplicadas, menos linhas), não porque o dump está corrompido. Nesse caso, trate o `FAILS` destas duas checagens especificamente como esperado (confira a saída linha a linha) e valide o dump antigo por outro meio equivalente — ex. confirmar manualmente que o ledger de migrations do restaurado bate com o que essa versão do checkout esperava NA ÉPOCA do dump, e que as checagens de RLS/isolamento (essas sim válidas para qualquer época) passaram.
5. **Aplique o restore real usando o MESMO `DUMP` validado no passo 4** — nunca um arquivo diferente escolhido só na hora do restore — seguindo o procedimento documentado em `docs/operations/` para este host (fora do escopo de código deste programa — decisão/execução do operador).
6. **Reconheça o run** depois de resolver:
   ```sh
   /opt/omnira/update-agent/bin/omnira-updatectl acknowledge-recovery --installation-id=<id> --run=<run_id>
   ```

## 3. RTO real medido (F8, staging isolado, nunca simulado "no papel")

Medições reais do ambiente de staging isolado (`docs/release/IMPLEMENTATION-STATUS.md`, seção F8, execução `f8-2963183`, 2026-10-22):

| Cenário | Classe observada | RTO real medido |
|---|---|---|
| Pull rejeitado pelo `ghcr.io` real (digest inexistente) | R0 | 3.06s |
| Baseline (deploy real + migration genuinamente pendente) | SUCCEEDED | 132.73s |
| Health check nunca passa (imagem broken) | R1 | 149.91s |
| Mesma falha + `recovery_class=R3` declarado | R3 | 114.90s |
| `kill -9` real do agente em pleno apply, recuperação por fencing | crash-resume (SUCCEEDED) | 136.07s |
| Migration drift (checksum divergente) | R0/R1 (ROLLED_BACK) | 7.08s |

Estes números são de um ambiente Docker Compose isolado em staging, não uma promessa de SLA de produção — reproduza com `make update-test-staging` antes de citar um RTO para um cenário real.

## 4. Gate de promoção a `stable` (quando o verificador de F9 estiver ligado ao pipeline de promoção real)

Uma release só é promovida a `stable` se, no momento da promoção: (1) `backup-restore-check.sh`/`backup-restore-hub-check.sh` tiver rodado com sucesso dentro de 24h; (2) nenhuma instalação estiver travada em `RECOVERY_REQUIRED`; (3) a release nunca declara `pitr_available=true` (seria falso, dado o estado real deste host). `GET /api/v1/platform/releases/retention` e `DELETE /api/v1/platform/releases/{release_id}` (F9) nunca removem uma release ainda referenciada por um run/plano ativo, ainda instalada, ou cuja evidência de retenção não foi revalidada no momento do delete — ver `08-troubleshooting.md`.
