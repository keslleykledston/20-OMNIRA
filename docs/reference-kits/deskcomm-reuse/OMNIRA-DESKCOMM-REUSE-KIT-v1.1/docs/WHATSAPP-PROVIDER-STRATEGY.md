# Estratégia de Provedores WhatsApp

## Objetivo

O OMNIRA deve suportar múltiplas implementações de WhatsApp sob um contrato canônico.

Categorias:

```text
official
unofficial
```

## Regra de produto

### Official
Caminho padrão/recomendado.

Exemplos:
- Meta WhatsApp Cloud API;
- BSPs oficiais.

### Unofficial
Caminho opcional por Tenant.

Exemplos possíveis:
- WAHA;
- providers baseados em sessão;
- providers compatíveis com WhatsApp Web;
- outros adapters futuros.

A arquitetura não presume um fornecedor específico.

## Modelo

```text
ChannelProvider
  provider_type: official | unofficial
  capabilities
  connection_mode
  risk_profile
```

Exemplo conceitual:

```text
WhatsAppChannel
 ├─ MetaCloudProvider          official
 ├─ OfficialBSPProvider        official
 ├─ WahaProvider               unofficial
 └─ OtherSessionProvider       unofficial
```

## Contrato canônico

Todo provider deve implementar, conforme capability:

```text
VerifyWebhook
ParseInbound
SendText
SendMedia
SendTemplate
DownloadMedia
HandleDeliveryStatus
CheckHealth
Connect
Disconnect
```

Nem todo provider precisa suportar todas as operações.

Capabilities devem ser consultáveis:

```text
text
media
template
delivery_status
read_status
typing
session_pairing
qr_pairing
voice
interactive_messages
```

## ChannelConnection

Adicionar metadados conceituais:

```text
provider
provider_type
status
capabilities
secret_ref
external_account_id
external_number_id
risk_acknowledged_at
risk_acknowledged_by
```

Campos específicos do provider ficam em configuração/adapters, não no core.

## Segurança multi-tenant

Para qualquer provider:

- uma conexão pertence a exatamente um Tenant;
- secrets são tenant-scoped;
- sessões não são compartilhadas;
- storage de sessão não pode ser global sem namespace/isolamento forte;
- worker sempre resolve `TenantContext`;
- fila contém IDs/refs, nunca segredo.

Para unofficial/session-based:

- volume/session store deve ser isolado por connection/tenant;
- QR/session token é segredo;
- endpoint de administração do provider não é público;
- reset/reconnect é auditado.

## UI

A tela de canais deve informar claramente:

```text
WhatsApp Oficial
WhatsApp Não Oficial
```

Não esconder essa diferença.

Para conexão não oficial:
- exibir aviso de risco;
- exigir confirmação do admin do Tenant;
- registrar aceite;
- permitir desativação imediata.

## Risco e compliance

O OMNIRA não deve presumir equivalência entre providers.

Cada adapter declara `risk_profile`.

Exemplo:

```text
official:
  account_risk: low/provider-governed

unofficial:
  account_risk: elevated
  support_model: best_effort
```

Não codificar texto jurídico rígido no domínio; manter mensagem configurável na UI/política do produto.

## Routing

Após normalização, o restante do sistema não deve saber se a mensagem veio de official/unofficial:

```text
Provider payload
 -> adapter
 -> canonical inbound message
 -> Contact
 -> Conversation
 -> Ticket
 -> Routing
 -> Automation
```

## Observability

Métricas devem incluir apenas dimensão de baixa cardinalidade:

```text
provider
provider_type
operation
status
```

Não usar tenant_id como label.

## Deploy

Providers não oficiais que exigirem runtime próprio devem rodar em containers separados.

Exemplo:

```text
omnira-api
omnira-worker
waha-provider       # profile opcional
```

Usar Docker Compose profile:

```text
profiles:
  - whatsapp-unofficial
```

O ambiente oficial não deve depender desse container.

## Failover

Não implementar failover automático entre official e unofficial no MVP.

Trocar provider é ação administrativa explícita porque:
- identidade/número;
- sessão;
- templates;
- capabilities;
- compliance;
podem divergir.

## DeskcommCRM

WAHA pode ser estudado e adaptado para um provider opcional.

Classificação:
`ADAPT + INSPIRE`

O adapter Meta Cloud permanece prioridade:
`PORT`
