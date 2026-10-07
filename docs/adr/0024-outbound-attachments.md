# ADR-0024: Anexos de saída (o operador envia arquivo ao cliente)

## Status
**Accepted e implementada (2026-10-07), desligada por padrão** (`OMNIRA_OUTBOUND_MEDIA_ENABLED=false`) até o teste ao vivo com um número de teste (ver
`docs/ops/OUTBOUND-MEDIA.md`). Resolve R-4 do `docs/architecture/mobile-readiness.md`; vale para o Web hoje e para o app depois (a API é a mesma).

## Contexto
O OMNIRA só enviava texto, template e menus. A mídia **recebida** já tem um pipeline hostil-por-padrão (ADR-0016: tipo pelo conteúdo, ClamAV, quarentena,
entrega segura). Faltava o caminho inverso, com a mesma postura: o arquivo sai do navegador de um operador para o celular de um cliente.

## Decisão
**Dois passos.** Tudo o que pode dar errado acontece no **upload**, onde a resposta ainda pode ser "não"; o **envio** é o mesmo caminho autorizar → enfileirar →
entregar do texto.

1. `POST /tenants/{t}/inbox/conversations/{c}/attachments` (`multipart/form-data`, parte `file`). Mesma autorização de enviar (`conversation.claim`,
   responsável ou `conversation.manage`, conversa aberta, canal ativo, janela de 24 h na Meta). Em ordem:
   - corpo em *streaming* com teto (16 MiB), **nunca** gravado em arquivo temporário; só os bytes da parte `file` ficam em memória;
   - **tipo pelo conteúdo** (mesma lista e mesmos limites do recebimento: JPEG/PNG/WebP/GIF, OGG/MP3/M4A/WAV/AMR/FLAC, MP4/WebM, PDF, texto), nunca pelo `Content-Type` nem pelo nome;
     arquivos compactados, executáveis, conteúdo ativo, *polyglot*, PDF com JavaScript e tipo declarado que contradiz o real são recusados;
   - **o que o provedor consegue entregar**: JPEG, PNG, MP4, PDF, texto e áudio OGG/MP3/M4A/AMR em ambos; WebP só no WAHA; GIF/WebM/WAV/FLAC não saem como mídia. Resto: `422 unsupported_for_channel`;
   - **metadados de imagem removidos** (EXIF/GPS/dispositivo, XMP, IPTC, comentários, *text chunks*) **sem recodificar** os pixels; imagem malformada é recusada, não enviada pela metade;
   - **ClamAV** sobre os bytes que serão enviados; vírus = `422`, antivírus fora = `503` (**fecha**: nada é aceito sem varredura);
   - nome mostrado ao cliente = nome do operador reduzido a letras/dígitos/separadores, sem caminho nem caracteres de controle ou bidirecionais, com a **extensão do tipo real**;
   - grava em `<media>/outbound/<tenant>/<id>` (só UUIDs no caminho) e registra em `message_outbound_media` (RLS forçada, `uploaded_by` obrigatório = quem fez o upload).
   Limites: 5 envios pendentes por operador por conversa (`409`), 20 uploads/min por operador (`429`), validade de 24 h se não for enviado.
2. `POST .../messages` com `attachment_id` (e `text` como legenda opcional, ≤ 1024 caracteres sem caracteres de controle). Uma instrução SQL **liga** o upload à mensagem **somente se** ele ainda é do
   operador, daquela conversa, sem mensagem, não expirado e não removido; só então cria a mensagem (tipo = `image|audio|video|document`, `mime_type`, `size_bytes`) e o job. Dois envios do mesmo
   upload: um perde (`422`). Idempotência por `Idempotency-Key` como no texto (repetição = `200` com a mesma mensagem). Um upload é de **um** operador: o supervisor não envia o de outro.
3. **Entrega** (worker): lê o arquivo do disco **conferindo tamanho e SHA-256** gravados; divergência = `failed: media_unavailable` (nada é enviado). WAHA: `sendImage|sendVideo|sendVoice|sendFile` com
   `file.data` em base64 (OGG vira nota de voz). Meta: `POST /{phone_number_id}/media` (repetível, sem efeito visível) e depois a mensagem com o `id` da mídia.
4. **Sem deduplicação por id**: nenhum dos dois provedores deduplica mídia (o `id` reservado só vale para texto no WAHA, e a Meta não tem chave). Portanto uma falha **ambígua** (tempo esgotado, 5xx,
   reset, resposta sem id) termina em **`uncertain`** e **nunca** é reenviada automaticamente (poderia entregar o arquivo duas vezes ao cliente); só limitação explícita (429) e rejeição provada são tratadas como tal.
5. **Retenção**: upload não enviado expira em 24 h (ou ao ser removido) e o *sweeper* do worker apaga arquivo e linha; arquivo enviado é removido após 60 dias (como o recebido) e a linha fica como registro.
   O operador vê o que enviou pelo mesmo endpoint de mídia (RLS decide; cabeçalhos hostis: `nosniff`, `Content-Security-Policy: sandbox`, `Cross-Origin-Resource-Policy`).
6. **Isolamento de escrita**: a API só escreve em `<media>/outbound` (montagem leitura-e-escrita aninhada sobre a montagem somente leitura do recebido); não consegue gravar em `clean/` nem `quarantine/`.

## Fora do escopo (decisões abertas para depois)
Vários arquivos por mensagem (envia-se um por vez), Office (docx/xlsx: recusados como no recebimento), gravação de voz no navegador, miniaturas, arrastar-e-soltar, envio para grupos, e a
reconciliação de `uncertain` de mídia (hoje manual, como o texto da Meta).

## Consequências
- Superfície nova: arquivos de operadores autenticados a clientes. A defesa é a mesma do recebimento (conteúdo, antivírus, nada executável) mais saneamento de metadados e propriedade do upload.
- A **API passa a precisar do ClamAV** (antes só o worker). Sem ele a funcionalidade nem liga (`Validate` recusa `OMNIRA_OUTBOUND_MEDIA_ENABLED=true` sem `OMNIRA_CLAMAV_ADDR` e `OMNIRA_MEDIA_DIR`).
- Disco: até 16 MiB × (pendentes 24 h + enviados 60 dias).
- Contratos: OpenAPI (`/attachments`, `attachment_id`, `can_send_media`); migration `000092`.
- Não validado ao vivo com os provedores reais (envio a um cliente de verdade): por isso desligado. Os testes cobrem o contrato HTTP de cada provedor com servidores simulados.
