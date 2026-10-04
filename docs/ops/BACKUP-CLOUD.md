# Backup externo: Google Drive criptografado

O `scripts/backup-omnira-db.sh` (cron, de hora em hora) copia os dumps para o disco USB e, se configurado, também para um Google Drive restrito. O passo de nuvem é opcional e **nunca derruba o backup local**: sem configuração ele imprime `cloud copy SKIPPED` e segue.

## Como funciona

- Dois remotes no arquivo dedicado `~/.config/omnira/rclone-backup.conf` (0600, fora do Git). O `~/.config/rclone/rclone.conf` do operador não é tocado.
  - `omnira-gdrive`: Google Drive com escopo `drive.file` (a aplicação só enxerga o que ela mesma criou) e lixeira desligada, para a retenção liberar espaço de verdade.
  - `omnira-backup`: `rclone crypt` por cima; nomes e conteúdo são cifrados antes de sair do host.
- Cada execução envia o que ainda não está na nuvem (`copy --ignore-existing`, então lacunas de execuções que falharam se corrigem sozinhas), confere os dumps recentes com `cryptcheck` (MD5 do Drive contra o arquivo local) e só então aplica a retenção de 30 dias na nuvem (local continua 7).
- Em caso de falha de upload ou de verificação a retenção não roda, e o log registra `== cloud copy FAILED`. Sucesso atualiza `backups/omnira_dev/.cloud-last-ok`.
- Variáveis (todas opcionais): `BACKUP_CLOUD_CONF`, `BACKUP_CLOUD_REMOTE`, `BACKUP_CLOUD_PATH`, `BACKUP_CLOUD_RETENTION_DAYS`, `BACKUP_CLOUD_TIMEOUT_SECONDS`, `BACKUP_CLOUD_VERIFY_MAX_AGE`.

## Ativar (uma vez)

O token OAuth exige um navegador. Em qualquer máquina com navegador e `rclone`:

```bash
rclone authorize "drive" --drive-scope drive.file
```

Entre com a conta Google que vai guardar os backups, copie o JSON que o comando imprime para um arquivo (`token.json`) e, neste host:

```bash
scripts/setup-backup-cloud.sh token.json     # cria o config, faz um ciclo de teste e imprime o aviso final
shred -u token.json                          # o token agora vive só no config
scripts/backup-omnira-db.sh                  # deve terminar com "cloud copy OK (client-side encrypted, checksum-verified)"
```

Se a conta Google for de uma organização, o administrador pode bloquear o app OAuth do rclone; nesse caso crie um OAuth client próprio no Google Cloud e adicione `client_id` e `client_secret` à seção `[omnira-gdrive]` do config.

## Guarde o config fora deste host

As senhas do `crypt` existem **somente** no arquivo de config. Sem elas, os backups da nuvem não podem ser lidos por ninguém. Copie `~/.config/omnira/rclone-backup.conf` para um cofre de senhas ou local lacrado logo após o setup. Nunca cole o conteúdo em chat ou no repositório. O setup recusa sobrescrever um config existente (`--force` troca as senhas e orfana o que já foi enviado).

## Restaurar a partir da nuvem

```bash
export RCLONE_CONFIG=~/.config/omnira/rclone-backup.conf
rclone lsf omnira-backup:omnira_dev                           # lista os dumps
rclone copyto omnira-backup:omnira_dev/<arquivo>.dump /tmp/restore.dump
```

Depois siga o restore já provado em `scripts/backup-restore-check.sh` (aplicar `roles.sql` antes do schema/dados; ver `docs/audit/GATE-INBOX-WAHA-LAB.md`).

## Testes

`scripts/test-backup-cloud.sh` usa um "Drive" local descartável com `crypt` real: envio, criptografia em repouso, restauração idêntica byte a byte, idempotência, falha sem retenção, setup e permissões. Não usa rede nem o banco vivo.

## Limites conhecidos

- Alerta externo: `scripts/backup-cloud-check.sh` roda a cada 15 min sob o wrapper ntfy (`run-check-with-alert.sh backup-cloud`, cron do usuário `suporte`). Lê só a idade de `.cloud-last-ok`: OK até 3 h, WARN entre 3 h e 6 h (silencioso), FAIL acima de 6 h, se nunca houve sucesso depois do setup, ou se o config sumiu depois de ter funcionado (ALERT uma vez, lembrete a cada 60 min, RECOVERY ao voltar). Sem config ele fica em WARN `cloud_not_configured`, que não notifica. Testes: `scripts/test-backup-cloud-check.sh` (29 verificações).
- O refresh token do Google pode ser revogado ou expirar (por exemplo, app em modo de teste no Google Cloud). Se o envio começar a falhar com erro de autenticação, rode `rclone authorize "drive" --drive-scope drive.file` de novo e troque **apenas** a linha `token =` da seção `[omnira-gdrive]` do config. Não use `--force` para isso: ele gera senhas novas de `crypt` e torna ilegível tudo que já foi enviado.

## Falhas intermitentes de cota do Google Drive (causa raiz, 2026-10-04)
Sintoma: `== cloud copy FAILED: upload did not complete` em várias rodadas seguidas do cron, e execuções manuais logo depois funcionando.
Causa medida (com `RCLONE_LOG_FILE`): `googleapi: Error 403: Quota exceeded for quota metric 'Queries' and limit 'Queries per minute'`
para `project_number:202264815644`, que é o **app OAuth compartilhado do rclone**. O remoto `omnira-gdrive` não tem `client_id` próprio,
então a cota de consultas por minuto é dividida com todos os usuários do rclone e estoura de forma intermitente.

- **Mitigação em vigor**: `_cloud_rc` repete (até 4 tentativas, esperas de 15, 30 e 60 s) **somente** quando a falha é de cota/limite de
  taxa; qualquer outro erro continua sendo reportado de imediato. Vale para o backup do banco e para o de mídia.
- **Correção definitiva (ação do dono da conta Google)**: criar um OAuth client próprio (Google Cloud Console > APIs e serviços >
  Credenciais > ID do cliente OAuth, tipo "App para computador", com a API do Google Drive ativada) e acrescentar `client_id` e
  `client_secret` à seção `[omnira-gdrive]` de `~/.config/omnira/rclone-backup.conf`; depois reautorizar o token
  (`scripts/setup-backup-cloud.sh --force` ou `rclone config reconnect omnira-gdrive:` com `RCLONE_CONFIG` apontando para esse arquivo).
  Não cole o segredo em conversas nem em commits.
- **Duplicatas**: o Drive permite dois objetos com o mesmo nome; o rclone avisa `Duplicate object found in destination - ignoring`
  para os dumps de 2026-10-02. É inofensivo para a cópia, mas a limpeza (`rclone dedupe`) é decisão do dono.
