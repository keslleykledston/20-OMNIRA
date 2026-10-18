# Roteiro de teste: CRM falando com o OMNIRA

Quem faz: a equipe do CRM (terminal 1 e 2) **junto com** quem tem o telefone de teste e a tela do OMNIRA (Keslley). Duração: cerca de 1 hora (o passo 14 é opcional e leva ~20 min).

**Regras do teste**
- Só a empresa e a linha de **teste** combinadas. Nada de cliente real.
- O segredo da credencial não vai para chat nem para print. Nas evidências, apague o token.
- Anote o horário e a saída de cada passo. Onde o resultado esperado não aparecer, **pare e avise**; não force.

## 0. Preparação

```bash
export OMNIRA_EXTERNAL_URL='https://omnira.devops.k3gsolutions.com.br/api/v1/external'
export OMNIRA_EXTERNAL_TOKEN='<o token que o administrador entregou>'
chmod +x ./bin/crm-sim-linux-amd64        # (Linux). No macOS: crm-sim-darwin-arm64 ou -amd64. No Windows: crm-sim-windows-amd64.exe
alias sim=./bin/crm-sim-linux-amd64
```

Terminal 2 (deixe aberto o teste inteiro): `sim poll 3` imprime cada fato novo do diário a cada 3 s.

## 1. Quem sou eu

| # | Ação | Esperado |
|---|---|---|
| 1 | `sim me` | `installation.status: active`; `scopes` com os 9 pedidos (leitura + `conversation.claim`, `message.send`, `conversation.transfer`, `conversation.finalize`, `media.read`); `features.read_api`, `events_api` e `write_api` = `true`; anote `rate_limit_per_min`, `lease_seconds` e a lista de filas/linhas permitidas |
| 2 | `sim queues` | uma linha `uuid  nome` por fila. Monte a tabela `etiqueta -> queue_id` do CRM com isto |

## 2. Ler e acompanhar

| # | Ação | Esperado |
|---|---|---|
| 3 | No **telefone de teste**, mande "oi" para a linha de teste. No terminal 2 observe | em até ~3 s: `conversation.created.v1` e `message.created.v1` (`origin=channel`). `sim list open` mostra a conversa com `control=unassigned` |
| 4 | `sim show <conversa>` | a mensagem "oi" (`inbound`, `received`) |

Guarde o `<conversa>` (uuid) e a versão de controle (`vN` em `sim list`).

## 3. Assumir e conversar

| # | Ação | Esperado |
|---|---|---|
| 5 | `sim claim <conversa>` | `200`; `control.state: held`, `holder.kind: crm_application`, `mine: true`; terminal 2: `conversation.control_changed.v1 ... mine=true`. **Na tela do OMNIRA** a conversa mostra o aplicativo externo e **não deixa responder** |
| 6 | Na tela do OMNIRA, tentar assumir/responder | recusado |
| 7 | `sim send <conversa> <versão> "Teste do CRM"` | **`202`, `delivery: queued`** e um `message_id`. **No telefone chega a mensagem.** Terminal 2: `message.created.v1 mine=true`, depois `message.status_changed.v1` (`sent`, `delivered`, `read` quando o telefone abrir) |
| 8 | Repita o passo 7 com a **mesma chave**: `SIM_KEY=teste-repetido-0001 sim send ...` duas vezes | a 2ª resposta é a mesma (`Idempotent-Replay`, **mesmo** `message_id`) e **uma só** mensagem no telefone |
| 9 | `sim send <conversa> <versão-menos-1> "versão velha"` | `412 stale_control_version`; nada chega ao telefone. O simulador mostra a versão atual: **releia antes de repetir** |

## 4. Arquivos

| # | Ação | Esperado |
|---|---|---|
| 10 | `sim attach <conversa> <versão> foto.jpg "Segue a foto"` com uma foto **com GPS nos metadados** | `uploaded: attachment_id=... kind=image`, depois `202 queued`. A foto chega ao telefone **com a legenda** e **sem** a localização (os metadados saem) |
| 11 | Repita com um PDF, uma nota de voz (`.ogg`/`.mp3`) e um vídeo (`.mp4`) | cada um chega ao telefone; anote o que **não** chegou e o erro (`422 media_unsupported` etc.) |
| 12 | Crie um arquivo de texto com a linha de teste padrão de antivírus (EICAR) e envie: `sim attach ...` | **recusado** (`422 file_blocked`/`file_rejected`); nada chega ao telefone |
| 13 | No **telefone**, mande uma foto ao CRM. Depois `sim show <conversa>` e `sim media <media_id> saida.jpg` | a mensagem aparece com `[image image/jpeg clean media=<id>]` (pode ficar `pending` por instantes); o arquivo baixa e abre |

## 5. Transferir, devolver, finalizar

| # | Ação | Esperado |
|---|---|---|
| 14 | `sim transfer <conversa> <versão> <queue_id de outra fila>` | `200`; a aplicação deixa de ser titular; terminal 2: `control_changed` (`unassigned` ou atribuída a uma pessoa da fila). Um novo `sim send` na mesma conversa dá `409 not_holder` |
| 15 | Em **outra** conversa (o telefone manda "oi" de novo ou use outra), `sim claim` e depois `SIM_NOTIFY_PROTOCOL=false sim finalize <conversa> <versão>` | `200`, `status: closed`. Tela do OMNIRA: conversa **finalizada**, protocolo **encerrado**. O telefone **não** recebe aviso de protocolo. Terminal 2: `conversation.closed.v1` |
| 16 | `sim release <conversa> <versão>` em uma conversa assumida | `200`, volta à fila; o roteador a distribui |

## 6. Falhas que o CRM precisa tratar

```bash
# sem credencial -> 401
curl -s -o /dev/null -w '%{http_code}\n' "$OMNIRA_EXTERNAL_URL/conversations"
# a empresa não é escolhida pelo pedido -> 400 tenant_not_accepted
curl -s -H "Authorization: Bearer $OMNIRA_EXTERNAL_TOKEN" -H 'X-Tenant-ID: 00000000-0000-0000-0000-000000000000' "$OMNIRA_EXTERNAL_URL/conversations" | head -c 300
# cursor do diário que o OMNIRA não emitiu -> 410 resync_required (o CRM deve refazer checkpoint + foto)
curl -s -H "Authorization: Bearer $OMNIRA_EXTERNAL_TOKEN" -w '\n%{http_code}\n' "$OMNIRA_EXTERNAL_URL/events?after=99999999999"
```

**Opcional (passo 17, ~20 min): perda do *lease*.** O administrador define `lease_seconds` curto (ex.: 60) para a credencial. Assuma uma conversa e **não** mande nada nem `heartbeat`: depois de *lease + 120 s* a conversa volta à fila. Terminal 2 mostra `conversation.control_changed.v1` (devolvida); o próximo `send` dá `412`/`409`. Repita com `sim heartbeat <conversa> <versão>` de 30 em 30 s e ela **não** expira.

**Suspensão (passo 18, com o administrador):** o administrador suspende a aplicação na tela. `sim me` passa a dar `401` na hora e as conversas que o CRM segurava voltam à fila.

## 7. O que devolver para nós

Saída (sem o token) dos passos 1, 2, 5, 7, 8, 9, 10-13, 14, 15; os fatos do terminal 2 durante o teste (arquivo de texto); foto da tela do telefone nos passos 7, 8, 10 e 11; e a lista do que **não** funcionou, com código e horário. Mande também a **tabela etiqueta -> fila** que o CRM vai usar e o **valor de `lease_seconds`** que o CRM prefere.

Com os passos 1-16 verdes, a leitura e a escrita ficam aprovadas para a fila de piloto. O que vier vermelho vira correção do lado do OMNIRA ou do adaptador antes de avançar.
