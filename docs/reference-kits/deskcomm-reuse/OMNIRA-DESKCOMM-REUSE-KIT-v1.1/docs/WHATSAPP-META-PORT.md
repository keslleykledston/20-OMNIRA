# Port Guide — WhatsApp Meta Cloud

## Deskcomm paths prioritários para auditoria

```text
lib/channels/types.ts
lib/channels/adapters/meta-cloud.ts
lib/channels/meta/
lib/channels/meta/ingest.ts
```

Pesquisar também:
- webhook parsing;
- credentials;
- templates;
- delivery statuses;
- phone variants;
- media persistence;
- channel health.

## OMNIRA target

```text
internal/channels/
  domain/
  application/
  ports/
  adapters/
    meta/
```

## Interface alvo conceitual

```text
ChannelProvider
  VerifyWebhook
  ParseInbound
  Send
  SendTemplate
  DownloadMedia
  CheckHealth
```

Não copiar shape TypeScript literalmente.

## Casos obrigatórios

- tenant resolvido por fonte confiável;
- nunca por payload arbitrário;
- `phone_number_id` é provider ref;
- dedupe por provider event/message id;
- retry-safe;
- mídia com allowlist/SSRF protection;
- token nunca em NATS;
- secret ref apenas;
- 429 tratado como transitório;
- auth inválida tratada como permanente/degraded;
- delivery/read statuses normalizados;
- window/template rules fora do adapter;
- E.164 normalization;
- Brasil com variações de nono dígito testadas.

## Eventos OMNIRA sugeridos

```text
evt.channel.message_received.v1
evt.channel.message_sent.v1
evt.channel.message_delivered.v1
evt.channel.message_read.v1
evt.channel.message_failed.v1
evt.channel.connection_degraded.v1
```

## NATS

Outbound:

```text
DB transaction
 -> message row
 -> outbox
 -> NATS
 -> whatsapp worker
 -> Meta adapter
```

Inbound:

```text
Meta
 -> webhook
 -> verify
 -> normalize
 -> transaction
 -> outbox
 -> downstream events
```

## Providers não oficiais

A existência deste port oficial não proíbe adapters não oficiais.

O contrato `ChannelProvider` deve ser genérico o suficiente para receber posteriormente:

```text
WahaProvider
SessionBasedProvider
OfficialBspProvider
```

Sem alterar Contact/Conversation/Ticket.

Regras:
- Meta Cloud continua primeira implementação de referência;
- unofficial é provider opcional;
- provider-specific session/QR fica no adapter;
- official e unofficial compartilham modelos canônicos, não implementação.
