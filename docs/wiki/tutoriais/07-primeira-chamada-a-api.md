# Tutorial 7: sua primeira chamada à API (integrador)

**Para quem:** equipe do CRM. **Tempo:** 15 minutos. **Você precisa de:** o token entregue pelo administrador (Tutorial 6) e um terminal.

A base é `https://omnira.devops.k3gsolutions.com.br/api/v1/external`. Toda chamada leva `Authorization: Bearer <token>`. A empresa é a do token: você nunca informa o `tenant_id`.

```bash
export OMNIRA_EXTERNAL_URL='https://omnira.devops.k3gsolutions.com.br/api/v1/external'
export OMNIRA_EXTERNAL_TOKEN='omn_ext_<id>.<segredo>'
H="Authorization: Bearer $OMNIRA_EXTERNAL_TOKEN"
```

## 1. Quem sou eu

```bash
curl -s -H "$H" "$OMNIRA_EXTERNAL_URL/me"
```

Devolve a instalação, os **escopos**, o limite de chamadas, o *lease*, as filas e linhas permitidas e as `features` do servidor. Guarde a resposta: ela explica qualquer `403` futuro.

## 2. As filas (setores)

```bash
curl -s -H "$H" "$OMNIRA_EXTERNAL_URL/queues"
```

Monte a tabela do CRM `etiqueta → queue_id` com isso.

## 3. As conversas abertas

```bash
curl -s -H "$H" "$OMNIRA_EXTERNAL_URL/conversations?status=open&limit=20"
```

Cada conversa traz `control` (quem atende e a **versão de controle**), `can` (o que a sua aplicação pode fazer agora) e `queue_id`. Para uma só, `GET /conversations/{id}` devolve também o cabeçalho `ETag: "control-N"`.

## 4. Siga as mudanças sem webhook

```bash
curl -s -H "$H" "$OMNIRA_EXTERNAL_URL/sync/checkpoint"         # { "seq_head": 1234, ... }
curl -s -H "$H" "$OMNIRA_EXTERNAL_URL/events?after=1234"       # fatos depois do checkpoint
```

Repita a segunda chamada a cada 2 a 3 segundos com `after` igual ao `next_cursor` da resposta anterior. Ignore `event_id` repetido. Um `410 resync_required` significa "refaça a partir do checkpoint". O guia explica cada fato.

## 5. Assumir e responder

```bash
CONV='<id da conversa livre>'
curl -s -X POST -H "$H" -H "Idempotency-Key: assumir-$CONV-0001" "$OMNIRA_EXTERNAL_URL/conversations/$CONV/claim"
# a resposta traz control.version (por exemplo 3); use-a no If-Match
curl -s -X POST -H "$H" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: msg-$CONV-0001" -H 'If-Match: "control-3"' \
  -d '{"text":"Olá, aqui é o atendimento."}' "$OMNIRA_EXTERNAL_URL/conversations/$CONV/messages"
```

- `202` significa **na fila de envio**, não "entregue". A entrega aparece nos fatos `message.status_changed.v1`.
- A mesma `Idempotency-Key` com o mesmo corpo devolve a **mesma** resposta: é como se repete com segurança depois de uma falha de rede.
- `412` ou `409`: a conversa mudou. **Releia** e decida de novo.

## 6. Sem escrever código: o simulador

O pacote traz o `crm-sim`, que faz tudo isso pela linha de comando, inclusive o laço de consulta:

```bash
./bin/crm-sim-linux-amd64 me
./bin/crm-sim-linux-amd64 queues
./bin/crm-sim-linux-amd64 poll 3          # imprime cada fato novo, a cada 3 s
./bin/crm-sim-linux-amd64 claim <conversa>
./bin/crm-sim-linux-amd64 send <conversa> <versão> "Teste do CRM"
```

Depois siga o [roteiro de teste](../integradores/roteiro-de-teste.md) e leia o [guia da API](../integradores/guia-da-api.md).
