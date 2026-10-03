# Dead-man switch da cadeia de cron

Os alertas do ntfy só funcionam enquanto o cron e os wrappers rodam. Se o cron parar, o wrapper quebrar ou o `notify.sh` falhar, ninguém é avisado e o silêncio parece saúde. O `scripts/deadman-ping.sh` (cron a cada 5 min) resolve isso: ele só dá um GET numa URL **externa** quando todos os jobs estão vivos. Se algum parar, o ping para e o vigia externo dispara o alerta sozinho.

"Vivo" = o log do job foi modificado dentro da idade permitida:

| Job | Frequência | Máximo |
|---|---|---|
| `nats-jetstream-check` | 5 min | 15 min |
| `waha-session-check` | 5 min | 15 min |
| `backup-cloud-check` | 15 min | 45 min |
| backup horário (`backups/omnira_dev/backup.log`) | 60 min | 90 min |

Log do próprio dead-man: `/var/log/omnira/deadman.log` (estados `ok`, `stale`, `ping_failed`, `not_configured`). A URL contém um token e nunca é impressa nem logada. Testes: `scripts/test-deadman-ping.sh` (23 verificações).

## Ativar (recomendado: healthchecks.io, automatizado)

O destino recomendado é o **healthchecks.io**: fica fora do servidor, então avisa também se o servidor inteiro cair, e a criação do check e das URLs é automática.

1. Crie uma conta gratuita em healthchecks.io e conecte onde quer ser avisado (e-mail e/ou celular) em *Integrations*.
2. Em *Settings → API Access*, crie uma chave **read-write** (a read-only não serve).
3. Grave a chave num arquivo, **fora do chat e do Git**:
   ```bash
   sudo install -m 600 -o suporte -g suporte /dev/null /etc/omnira/healthchecks.env
   printf 'HC_API_KEY=%s\n' '<cole-a-chave>' > /etc/omnira/healthchecks.env
   ```
4. Rode `scripts/setup-deadman-healthchecks.sh`. Ele cria (ou reaproveita, pelo nome) o check "OMNIRA cron chain" com período de 5 min e tolerância de 10 min, anexa seus canais de alerta, grava `DEADMAN_URL` e `DEADMAN_FAIL_URL` em `/etc/omnira/notify.env` (0600, preservando o resto) e dispara o primeiro heartbeat. A chave e as URLs nunca são impressas. Testes: `scripts/test-setup-deadman-healthchecks.sh` (33 verificações, contra uma API simulada).

O cron `*/5` do `deadman-ping.sh` já está instalado e passa a pingar assim que `DEADMAN_URL` existir. O script reescreve o `notify.env` no próprio arquivo (o diretório `/etc/omnira` é de root e não aceita arquivos novos), preservando dono e permissão.

### Manual (sem a chave de API)

Crie o check você mesmo (period 5 min, grace 10 min), copie a ping URL e coloque em `/etc/omnira/notify.env`:

```
DEADMAN_URL="<ping-url>"
DEADMAN_FAIL_URL="<ping-url>/fail"
```

### Por que não o Uptime Kuma

Há um Uptime Kuma neste host, mas ele **não serve bem como vigia**: `kuma.devops.k3gsolutions.com.br` cai no nginx do OMNIRA (existe uma rota do Traefik em arquivo, mas não há Traefik rodando), então o Kuma só responde dentro da rede Docker, num IP que muda a cada recriação. Além de ficar no mesmo servidor que se quer vigiar, não há URL estável para o ping. Se a rota pública do Kuma for consertada, ele pode ser somado: `DEADMAN_URL` aceita várias URLs separadas por espaço.
