# Mídia recebida: captura, quarentena e antivírus (ADR-0016, M1)

Todo anexo recebido por WhatsApp é tratado como hostil até provar o contrário. O WAHA apaga a cópia dele em poucos
minutos, então a captura é imediata.

## Fluxo
`messages` (entrada com mídia) → trigger cria `message_media` (`pending`) na mesma transação → worker (a cada 2 s)
baixa do WAHA → classifica pelos **bytes reais** (lista de permissão) → grava em **quarentena** → ClamAV →
`clean` (move para a área servida) | `infected` | `rejected`. Falha em qualquer etapa **fecha**: nada é liberado.

| status | significado | arquivo no disco | a API entrega |
|---|---|---|---|
| `pending` | ainda não baixado | não | 409 |
| `quarantined` | baixado, aguardando antivírus (ou antivírus fora do ar) | quarentena | 409 |
| `clean` | liberado | área `clean/` | 200 (inline: imagem/áudio/vídeo; resto: anexo) |
| `infected` | antivírus detectou | **apagado** | 403 |
| `rejected` | tipo fora da lista, conteúdo ativo, limites | **apagado** | 403 |
| `source_gone` | o WAHA já não tinha o arquivo (mídia anterior à M1 também) | não | 410 |
| `failed` | esgotou tentativas (ex.: antivírus indisponível por ~1 dia) | quarentena (mantido) | 403 |

Aceitos: JPEG/PNG/GIF/WebP, OGG/MP3/M4A/WAV/FLAC/AMR, MP4/WebM/MOV, PDF sem JavaScript/Launch/anexos, texto simples.
Recusados: ZIP/RAR/7z/gzip e Office (OOXML é ZIP; legado é OLE2), executáveis, scripts, HTML/SVG/XML, PDF ativo, imagem
com marcação de script embutida, bomba de pixels (> 12 MP), > 25 MiB, tipo declarado diferente do real.

## Onde fica
- Diretório: `/opt/omnira-media` (host) = `/var/lib/omnira/media` nos contêineres `api` (somente leitura) e `worker`.
  `quarantine/<tenant>/<id>` e `clean/<tenant>/<id>`; nome = UUID, nunca o nome do remetente. Modo 0700/0600.
- Mapeamento: `docker-compose.override.yml` (local). O padrão do repositório é o volume nomeado `media_data`.
- Fora do dump do banco: o backup do banco guarda estado e hashes, **não** os arquivos. Backup dos arquivos: pendente (M8).
- Retenção: arquivo 60 dias (`Retention`); depois o arquivo some e a linha fica (`file_purged_at`). O texto extraído
  (M2+) permanece na conversa.

## Cache no navegador
Imagem e áudio liberados saem com `Cache-Control: private, max-age=86400` e `ETag` (SHA-256 do conteúdo): o navegador do
operador não baixa de novo ao reabrir a conversa e revalida (304) depois de 24 h. Vídeo e documentos: `no-store`. Limite
conhecido: uma cópia fica no cache do navegador até expirar, mesmo se o arquivo for removido pela retenção ou bloqueado depois.

## Antivírus
Serviço `clamav` (imagem `clamav/clamav:1.4`, ~1 GB de RAM, limite 2 GB, rede interna, sem porta publicada). Assinaturas
atualizadas pelo `freshclam` da própria imagem; o healthcheck é o da imagem (`clamdcheck.sh`, primeira subida ~1-2 min).
- Métrica do worker (`:9090/metrics`): `omnira_media_antivirus_up` (1/0) e `omnira_media_events_total{stage,outcome}`.
- **Antivírus fora do ar**: os arquivos ficam em `quarantined` e são reprocessados com recuo (10 s até 15 min); nada é
  liberado sem veredito. Após ~60 tentativas viram `failed`. Ação: `docker compose up -d clamav` e conferir o log.
- Veredito `infected` gera `audit_events` (`media.infected`, `outcome=failure`, assinatura em `metadata.reason`).

## Verificação rápida
```bash
docker compose exec -T postgres psql -U omnira -d omnira_dev -c \
  "select status, count(*) from message_media group by 1 order by 2 desc;"
docker exec omnira-worker wget -q -O - http://localhost:9090/metrics | grep media
```

## Reverter
`docker tag 20-omnira-api:rollback-pre-media-m1-20261004 20-omnira-api:latest` (idem `worker`), `docker compose up -d
--no-deps api worker`; a migration 000060 tem `down` (remove tabela e triggers). A API sem `OMNIRA_MEDIA_DIR` volta ao
modo antigo (busca ao vivo no WAHA, que só funcionava nos primeiros minutos).
