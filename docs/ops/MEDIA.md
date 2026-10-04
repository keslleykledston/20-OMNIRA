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

## Transcrição de áudio (M2) e texto derivado
O áudio liberado gera um job em `message_media_analysis` e é transcrito localmente (`docs/ops/WHISPER.md`). O texto pertence à
**mensagem**, não ao arquivo: continua na conversa depois que o arquivo é removido pela retenção de 60 dias. Só o worker escreve; os
operadores só leem (RLS). A interface mostra "Transcrição automática · IA local" como texto simples (sem markup nem links).

## Backup dos arquivos (M8)
`scripts/backup-omnira-media.sh` (cron `:35` de cada hora) copia **só `clean/`** (nunca a quarentena) de forma incremental e conferida por
checksum para o disco externo (`/mnt/omnira-backup-external/Backup/omnira_media`) e para a nuvem cifrada (`omnira-backup:omnira_media`,
rclone crypt, mesma configuração dos dumps). Retenção nas duas: 90 dias (60 de arquivo + 30 de margem). Marcadores em
`backups/omnira_media/` (`.external-last-ok`, `.cloud-last-ok`) alimentam `scripts/backup-media-check.sh` (`*/15`, via
`run-check-with-alert.sh`: AVISO após 3 h, FALHA após 6 h sem backup na nuvem). Prova descartável: `scripts/test-backup-media.sh`.
**Restaurar**: copie `Backup/omnira_media/<tenant>/<id>` de volta para `/opt/omnira-media/clean/<tenant>/<id>` (modo 0600, dono uid 1000);
o banco guarda estado e hash (`message_media.sha256`) para conferir.

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

## Imagens e PDFs (Gemini, por tenant) — ADR-0017 onda 9

- **Desligado por padrão.** Só roda para tenant cujo administrador informou a chave Gemini, registrou o consentimento e ligou a integração em *Configurações → Inteligência artificial externa*. Sem isso **nenhuma linha de análise é criada** e nada sai do servidor. Áudio continua 100% local (Whisper).
- Só arquivos **liberados pelo antivírus** e com mime verificado nos bytes: `image/jpeg`, `image/png`, `image/webp` → `description`; `application/pdf` → `document_text`. GIF, vídeo e áudio nunca vão. Janela de 7 dias (retenção do arquivo); ao ligar a integração, mídia recente ainda no disco é analisada.
- A chave do tenant é lida (decifrada) só no momento da chamada, vai apenas no header `x-goog-api-key`, nunca em URL/log. Sem redirects, resposta limitada, sem ferramentas, `temperature 0`, instrução fixa que trata o anexo como **dado**. O texto devolvido é sanitizado e marcado `suspicious` se parecer instrução; nunca é obedecido.
- **Orçamento** (US$ 10/mês por padrão, por tenant) checado **antes** de cada chamada contra o pior caso dela (modelo desconhecido é precificado como o mais caro); estourou → `skipped` (`budget_exceeded`). Cada chamada, com ou sem sucesso, vai ao ledger (onda 10). Preços em `internal/media/domain/pricing.go` são **estimativas**; a fatura do Google é a verdade.
- Provedor fora do ar/limite → retry com backoff (até 8 tentativas); chave recusada → `failed` (`provider_rejected`); nada legível → `empty`.
- Lacuna conhecida: depois que a leitura termina, a mensagem só-mídia **não é reroteada** automaticamente (o contexto do tópico já usa o texto lido).
