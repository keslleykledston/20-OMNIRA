# Fuso horário (Manaus) - política e como reverter

Vigente desde 2026-10-04. Manaus é UTC-4 e não tem horário de verão desde 2019.

## Regra
**Instantes** (carimbos gravados no banco, em eventos, em cursores de paginação e nas respostas da API) são sempre o
mesmo instante absoluto. **Texto escrito para uma pessoa** (e-mail, log legível, agendamento) usa o horário de Manaus.

## O que está em Manaus
| Onde | Como | Reverter |
|---|---|---|
| Servidor (cron, journal, `date`) | `timedatectl set-timezone America/Manaus`; `cron` reiniciado | `sudo timedatectl set-timezone Etc/UTC && sudo systemctl restart cron` |
| Contêineres `api`, `worker`, `web` | `TZ: America/Manaus` em `docker-compose.override.yml` (arquivo local, não versionado) | remover as linhas `TZ` e `docker compose up -d --no-deps --force-recreate api worker web` |
| Banco (sessões novas) | `ALTER DATABASE omnira_dev SET timezone TO 'America/Manaus'` | `ALTER DATABASE omnira_dev RESET timezone` |
| E-mail de convite | "dd/mm/aaaa hh:mm (horário de Manaus)" | - |
| Log do job de arquivamento de grupos | horário local com deslocamento (`-0400`) | - |
| Interface | segue o fuso do navegador (operadores em Manaus veem Manaus) | - |

## O que continua em UTC, de propósito
- **Contêineres de infraestrutura** (Postgres, Keycloak, WAHA, NATS, Valkey): não foram reiniciados. Reiniciar o WAHA
  arrisca derrubar a sessão real do WhatsApp; os logs deles seguem em UTC.
- **Nomes dos arquivos de backup** (`omnira_dev_20261004T171124Z.dump`): o `Z` os torna inequívocos e ordenáveis.
- **Saída dos monitores** (`<carimbo-UTC> OK|WARN|FAIL ...`): é o contrato lido por `run-check-with-alert.sh`.

## Agendamentos
Todos os crons existentes são periódicos (a cada N minutos ou uma vez por hora no minuto X), então mudar o fuso não os
deslocou. O arquivamento de grupos roda às **03:30 de Manaus** (`30 3 * * *`).

## Formato dos horários na API
Mistura de `...Z` e `...-04:00` conforme o campo (o `pgx` devolve no fuso do processo). Os dois são RFC 3339 e o mesmo instante;
quem consome deve sempre interpretar o deslocamento, nunca assumir `Z`.
