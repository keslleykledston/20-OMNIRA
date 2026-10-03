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

## Ativar

Coloque a URL em `/etc/omnira/notify.env` (mesmo arquivo do ntfy, fora do Git):

```
DEADMAN_URL=<URL de ping do vigia>
# opcional: DEADMAN_FAIL_URL=<URL chamada na hora em que um job para>
```

O cron já está instalado; sem `DEADMAN_URL` ele só registra `not_configured`.

### Opção A: Uptime Kuma (já roda neste host, porta 3001)

1. Add New Monitor, tipo **Push**.
2. Heartbeat interval **600 s** (o cron pinga a cada 300 s) e um retry; anexe a notificação (por exemplo ntfy).
3. Copie a **Push URL** e use como `DEADMAN_URL`. Para alerta imediato, use a mesma URL com `status=down` em `DEADMAN_FAIL_URL`.

Limite: se o host inteiro cair, o Kuma cai junto e ninguém avisa.

### Opção B: healthchecks.io (fora do host)

Crie um check com period **5 min** e grace **10 min**, e use a ping URL como `DEADMAN_URL` (`/fail` como `DEADMAN_FAIL_URL`). Cobre queda do host inteiro. Sai do host apenas a requisição de ping, sem dados do OMNIRA.

### Recomendação

A e B juntas: o Kuma pega falha de cron/script com alerta rápido, e o healthchecks.io pega a queda do host. Hoje o script suporta uma URL; para as duas, aponte `DEADMAN_URL` para uma e peça para o Kuma monitorar a outra, ou me peça para aceitar uma lista de URLs.
