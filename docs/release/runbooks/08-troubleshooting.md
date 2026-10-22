# Troubleshooting

Sintomas comuns e como diagnosticar, usando os sinais reais de `GET /api/v1/platform/monitoring/status` (F9) e o `updatectl status` local.

## Leitura rápida: `GET /api/v1/platform/monitoring/status`

| Campo | O que significa | Default seguro quando nunca observado |
|---|---|---|
| `agent_heartbeat_seen` / `agent_stale` | o daemon grava um heartbeat a cada 30s (independente do ciclo de reconcile — nunca parece obsoleto durante um backup/`--observe-window` genuinamente longo) | sem heartbeat nunca visto → tratado como obsoleto |
| `backup_evidence_present` / `_verified` / `_stale` | evidência de backup/restore registrada (`POST /api/v1/platform/backups`) dentro da janela da política | sem evidência → `stale=true` |
| `version_divergence` | versão/digest **reais observados** nos containers divergem do que `platform_installations` espera | comparação só acontece depois de uma sincronização fresca com o ledger — nunca compara contra um valor já sabido obsoleto |
| `observed_digests_stale` | o agente nunca conseguiu observar os containers reais (`exec.Compose.CurrentSnapshot`), ou a última observação é antiga | **fail-safe para `true` (obsoleto)** quando nunca houve observação bem-sucedida — achado Codex F9: o default anterior (`false`) fazia uma instalação que nunca sincronizou parecer "digest confiável" |
| `stalled_run_id`/`_state`/`_since` | um run ficou parado num estado não-terminal por tempo anormal | sem runs ativos → vazio |
| `unresolved_recovery_required` | há um `RECOVERY_REQUIRED` pendente nesta instalação | — |

## "status" avisa `RECOVERY_REQUIRED` e nenhum run novo avança

```
ATENÇÃO: há um RECOVERY_REQUIRED não reconhecido nesta instalação — nenhum run novo será processado até 'acknowledge-recovery'
```
**Causa:** um run anterior terminou em `RECOVERY_REQUIRED` (dado incompatível, rollback automático inseguro) e ainda não foi investigado/reconhecido. **Ação:** siga `04-restore-e-pitr.md` até confirmar que o dado está consistente, depois `updatectl acknowledge-recovery --run=<id>` (`03-rollback-e-recovery.md`). Nunca pule esta etapa criando um novo run — o gate existe exatamente para impedir isso.

## Lock da instalação "preso" (outro processo não consegue adquiri-lo)

**Causa provável:** um processo anterior (`updatectl`/daemon) morreu sem liberar o lock (`--lease`, default 5min) e o lease ainda não expirou, ou dois holders estão competindo de verdade. **Diagnóstico:** `updatectl status --installation-id=<id>` mostra o run/estado atual; o holder atual aparece no journal de checkpoints. **Ação:** espere o lease expirar (daemon/CLI reassume automaticamente — fencing real testado em F8, cenário de `kill -9`), ou confirme que não há um processo real ainda rodando (`ps aux | grep omnira-update`) antes de qualquer ação manual no banco/ledger.

## `start` recusa com "run criado" apontando para um run antigo já resolvido

**Causa:** antes de uma correção de F6/updatectl, reenviar `start` para a mesma release sempre deduplicava pela `release_id`, mesmo depois do run anterior já ter terminado (sucesso ou falha). **Comportamento atual (correto):** retry depois de um run **terminal** gera uma tentativa genuína nova; retry enquanto o run anterior ainda está **ativo** (ou já `SUCCEEDED`) é deduplicado para o mesmo run. Se isto não bater com o observado, confira a versão do binário `updatectl` em uso (`idempotencyKeyForRetry`, `apps/updatectl/cmd/omnira-updatectl/main.go`).

## `omnira-update-agent` não inicia depois de instalado via systemd

**Causa mais comum (achado real de Codex, F9):** `--ledger-path` default não bate com o `StateDirectory` que o systemd criou — ver `01-primeira-instalacao-agente.md` §"Erro comum". **Diagnóstico:** `journalctl -u omnira-update-agent -n 50` mostra o erro exato de abertura do ledger (permissão negada ou diretório inexistente).

## Instalação standalone "nunca confiável" mesmo com a API de volta

**Causa:** a marca `standalone` residual não foi limpa. **Ação:** suba o agente/CLI sem `--standalone`/`OMNIRA_AGENT_STANDALONE` — `agentconfig.Config.Build()` limpa a marca automaticamente a cada montagem (idempotente). Se persistir, confirme que a variável de ambiente não está sendo herdada de algum `EnvironmentFile`/wrapper esquecido.

## `reconcile`/`rollback` travam esperando `--database-url`

**Causa:** `reconcile` (que pode migrar até `MIGRATING_EXPAND`) exige `--database-url`/`OMNIRA_AGENT_DATABASE_URL` explicitamente; `rollback` **não exige** (nunca migra). Se `reconcile` falhar por isto num cenário de emergência sem acesso ao schema owner, use `rollback` (reverte código/config) em vez de tentar forçar uma migração sem credenciais.

## `DELETE /api/v1/platform/releases/{release_id}` recusa com `422`

**Causa:** a release ainda está dentro da janela de retenção, foi revogada, está referenciada por um run/plano ativo, está instalada atualmente, ou ainda é fornecida pelo histórico do ledger local de alguma instalação — **todos revalidados no momento do delete**, nunca confiando só no relatório de `GET /api/v1/platform/releases/retention` (que é só leitura, nunca a autoridade final). Isto é o comportamento esperado de um garbage collector seguro — não é um bug a contornar forçando o delete por outro caminho.

## Suíte de staging (F8) falha localmente

Rode com `make update-test-staging` (equivalente a `bash scripts/release/test-f8-staging.sh`) — requer Docker com permissão real (grupo `docker` ou `sudo -n` configurado) e portas livres para a topologia isolada. Falhas comuns documentadas durante o desenvolvimento de F8 (todas já corrigidas no script atual, mas úteis para diagnosticar um ambiente diferente): `pg_isready` sem `-h` valida o socket Unix em vez do listener TCP real; `kill -9 "$PID"` sem `pkill -f --holder=...` mata só o wrapper do shell, não o processo real que detém o lock. Evidência de cada rodada fica em `/tmp/omnira-f8-reports/$RUN` mesmo sem `KEEP_WORK=1`.
