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
   - JPEG e PNG são **decodificados por inteiro** (truncado/corrompido = recusado), tudo depois do EOI/IEND é cortado, metadados entre *scans* de JPEG progressivo também saem, WebP confere o tamanho do contêiner e nomes PDF com `#xx` são normalizados antes de procurar JavaScript/Launch;
   - **ClamAV** sobre os bytes que serão enviados; vírus = `422`, antivírus fora = `503` (**fecha**: nada é aceito sem varredura);
   - nome mostrado ao cliente = nome do operador reduzido a letras/dígitos/separadores, sem caminho nem caracteres de controle ou bidirecionais, com a **extensão do tipo real**;
   - grava em `<media>/outbound/<tenant>/<id>` (só UUIDs no caminho) e registra em `message_outbound_media` (RLS forçada, `uploaded_by` obrigatório = quem fez o upload).
   Limites (contados no banco, valem entre réplicas): 5 pendentes por operador por conversa, 64 MiB de bytes não enviados por operador, 2 GiB de arquivos guardados por tenant (`409`), 30 uploads/min por operador (`429`; mais 20/min em memória por processo), validade de 24 h se não for enviado.
   **O corpo é lido antes da sessão do tenant** (`BufferUpload`): a sessão segura uma conexão do banco enquanto o handler roda, então um cliente lento não pode prender conexões; ele ocupa só um dos 4 *slots* de upload (memória) e vale um prazo de leitura de 60 s. A decodificação completa da imagem (até 12 MP, ~50 MiB) tem seu próprio limite de 2 simultâneas.
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

## Revisão independente (Codex, `read-only`, 2026-10-07)
Duas passadas, 0 BLOCKER. Primeira (4 HIGH, 4 MEDIUM, 3 LOW): validação estrutural e *polyglot*, memória de uploads concorrentes, redirecionamentos nos clientes, corrida *sweeper*×envio, policy de UPDATE, cota atômica, retenção de mensagens pendentes, disponibilidade do provedor, *symlink*, órfãos: todos válidos e corrigidos (o `.changes/` do `AGENTS.md` não existe neste repositório, que usa `CHANGELOG.md`). Segunda (3 HIGH, 5 MEDIUM, 2 LOW): orçamento de memória da decodificação e do base64, provedor desabilitado barrado também no backend, cota durável em bytes e por minuto, metadados entre *scans* progressivos, `CheckRedirect` para clientes injetados, transições estritas na *trigger* (expiração só encurta, purga só do worker), órfãos de registros já purgados, *defer* do slot: corrigidos. Aceito e documentado: `O_NOFOLLOW` só no arquivo final servido (os diretórios-pai pertencem à aplicação; o volume não é gravável por operadores nem por clientes).
O leitor do arquivo enviado (para o operador ver o que mandou) não confere o hash a cada leitura; só o worker confere antes de entregar.
