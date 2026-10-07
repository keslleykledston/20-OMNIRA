# Anexos de saída: operação (ADR-0024)

Desligado por padrão. Quando ligado, o operador vê o clipe no campo de resposta (só em conversas cujo provedor entrega arquivos: WAHA e Meta Cloud).

## Ligar
1. Diretório de uploads, dono uid 1000, modo 700 (o mesmo uid dos contêineres `api` e `worker`):
   `install -d -m 700 -o 1000 -g 1000 <OMNIRA_MEDIA_HOST_PATH>/outbound` (em produção: `/opt/omnira-media/outbound`).
2. `.env`: `OMNIRA_OUTBOUND_MEDIA_HOST_PATH=<OMNIRA_MEDIA_HOST_PATH>/outbound` e `OMNIRA_OUTBOUND_MEDIA_ENABLED=true`. O ClamAV precisa estar de pé (`OMNIRA_CLAMAV_ADDR`, padrão `clamav:3310`): a `api` recusa subir ligada sem ele.
3. Recriar `api` e `worker` (o `worker` ganha a montagem `outbound` e o *sweeper*): `docker compose up -d --no-deps --force-recreate api worker`.
4. Migration `000092` aplicada antes (tabela `message_outbound_media`).

## Teste ao vivo obrigatório antes de liberar aos operadores (precisa de um número de teste do dono)
Envie para um número seu: uma foto JPG com GPS (a foto recebida **não** deve ter localização), um PDF, um áudio OGG (chega como nota de voz no WAHA), um MP4; confirme na conversa e no celular;
confirme que o EICAR (`X5O!P%@AP...`) é recusado com "antivírus"; pare o `clamav` e confirme `503`. Em linha Meta, repita com JPG e PDF. Registre o resultado aqui.

## Operar
- Métricas/logs: `messages: attachment accepted ...`, `messages: upload blocked by the antivirus ...`, `channel delivery: uncertain ...`, `outbound media: swept ...`.
- `uncertain` de mídia: o arquivo **pode** ter chegado; confira no celular antes de reenviar (o sistema nunca reenvia sozinho).
- Disco: `du -sh <host path>/outbound`; pendentes expiram em 24 h, enviados em 60 dias.
- Desligar: `OMNIRA_OUTBOUND_MEDIA_ENABLED=false` e recriar a `api`. Mensagens de mídia já enfileiradas continuam sendo entregues pelo worker.
- Reverter a migration: `down` remove só a tabela de registros; apague `outbound/` à mão se quiser liberar disco.
