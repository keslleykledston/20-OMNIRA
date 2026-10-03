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

- Não há alerta externo para falha do envio ainda; o sinal é a linha `cloud copy FAILED` no `backup.log` e a idade de `.cloud-last-ok`. Cobrir isso com o wrapper ntfy existente é o próximo passo natural.
- O refresh token do Google pode ser revogado ou expirar (por exemplo, app em modo de teste no Google Cloud). Se o envio começar a falhar com erro de autenticação, rode `rclone authorize "drive" --drive-scope drive.file` de novo e troque **apenas** a linha `token =` da seção `[omnira-gdrive]` do config. Não use `--force` para isso: ele gera senhas novas de `crypt` e torna ilegível tudo que já foi enviado.
