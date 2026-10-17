# 17 — Pedido ao fornecedor do ERP/CRM da K3G: o que precisamos para criar Ordem de Serviço pelo OMNIRA

Para enviar à equipe do CRM/ERP (K3G). Responder item a item; "não existe" é uma resposta válida. Detalhes técnicos do nosso lado: `16-erp-provider-contract.md`.

**Contexto em uma frase:** hoje o OMNIRA cria **chamados** (`/api/support/tickets`) e lê a lista de empresas. Para criar **Ordem de Serviço (OS)** a partir de um atendimento, sem duplicar OS quando a rede falhar, precisamos dos itens abaixo. Regra nossa: o ERP só recebe registro quando há tarefa, ocorrência ou OS; um atendimento sozinho nunca cria nada lá.

## Perguntas (responder todas)

| # | Pergunta | Resposta |
|---|---|---|
| 1 | Existe endpoint para **criar OS**? Qual método/caminho? Quais campos são obrigatórios (cliente, tipo, título, descrição, prioridade…)? Devolve o id da OS criada? | |
| 2 | Existe endpoint para **ler uma OS por id**? Devolve status e data da última atualização? | |
| 3 | A criação aceita uma **chave de idempotência ou correlação** (`Idempotency-Key`, `externalReference` ou `correlationId`)? Ela volta na leitura? Se mandarmos a mesma chave duas vezes, o ERP devolve a mesma OS ou cria outra? | |
| 4 | É possível **buscar a OS pela chave de correlação** (ex.: `GET /os?correlationId=…`)? | |
| 5 | **Erros:** quais códigos HTTP para cliente inexistente, validação, sem permissão, indisponível? Confirmam que 4xx = recusa definitiva e 5xx/timeout = "não sei se criou"? | |
| 6 | **Status da OS:** lista de códigos e o que cada um significa (aberta, em andamento, concluída, cancelada…). | |
| 7 | **Atualizações:** o ERP avisa por webhook assinado quando a OS muda, ou há consulta incremental (`updatedAfter`)? | |
| 8 | Existe **ambiente de teste (sandbox)** separado da produção, com credencial própria? Criar OS no teste não pode gerar visita nem cobrança. | |
| 9 | **Limites:** requisições por minuto, tamanho máximo de título e descrição. | |
| 10 | **Credencial:** que tipo (token, chave de API) e quem renova? Ela é por empresa cliente ou única da K3G? | |
| 11 | O mesmo identificador de **cliente/empresa** que o OMNIRA já usa nos chamados vale para a OS? | |

## Exemplo do que mandaríamos (ajustar ao seu formato)

```http
POST /api/<caminho-da-os>
Idempotency-Key: omnira-<uuid-do-pedido>
Content-Type: application/json

{ "customerId": "<id da empresa no CRM>", "title": "Sem conexão", "description": "…", "priority": "normal",
  "externalReference": "KTG-1007" }
```

Resposta esperada (ilustrativa: `externalReference` só volta se o ERP aceitar uma chave de correlação, item 3; sem ela basta o id e o status): `201` com `{ "id": "<id da OS>", "status": "<código>", "updatedAt": "<ISO-8601>", "externalReference": "KTG-1007" }`.

## Se faltar idempotência (itens 3 e 4)

Funciona, mas sem automação: quando a resposta de uma criação se perder (queda, timeout), o OMNIRA **bloqueia novo pedido daquele atendimento** até uma pessoa conferir no ERP e resolver à mão. É seguro, só não é automático. Com os itens 3 e 4 a conferência vira automática.

## O que acontece depois das respostas

1. Escrevemos o adaptador e rodamos o teste de contrato contra o **sandbox** (`gatewaytest.RunContract`).
2. Revisão do Codex no adaptador.
3. Só com ordem do dono: ligar `OMNIRA_SERVICE_EXTERNAL_ORDERS_ENABLED`, definir quais papéis podem pedir OS, smoke com número de teste. Desligar = flag `false`; as OS já criadas permanecem.
