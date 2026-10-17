# P2 do piloto headless — empresa real, CRM real (roteiro para o dia em que o dono estiver presente)

Pré-requisitos (todos precisam estar "sim" antes de começar; nada aqui roda sozinho):

| # | Pré-requisito | Como conferir |
|---|---|---|
| 1 | P0 e P1 limpos (ver `HEADLESS-PILOT-PLAN.md`), inclusive retomada pelo supervisor e suspensão/revogação pela tela | o dono confirmou |
| 2 | Você (ou um supervisor) acompanha as **primeiras 24 h** | combinado |
| 3 | Empresa escolhida e o **integrador identificado** (quem opera o CRM, contato de emergência) | decisão do dono |
| 4 | Fila e linha do piloto definidas; conversa de clientes reais só entra nelas de propósito | decisão do dono |
| 5 | O CRM tem um endpoint HTTPS **público** (porta 443 ou 8443, sem credencial na URL) para receber os eventos | o integrador informa a URL |
| 6 | Backup do banco **e** da mídia feitos na hora (`scripts/backup-omnira-db.sh`, `scripts/backup-omnira-media.sh`) | scripts imprimem `copy OK` |
| 7 | `scripts/headless-pilot-check.sh` mostra tudo zerado ou esperado, e `/var` com folga | rodar |

## Passos

1. **Aplicação do CRM** (Integrações → Nova aplicação, na empresa escolhida): começar **só leitura** (`conversation.read`, `message.read`, `contact.read`, `events.read`), filtrada pela fila e pela linha do piloto. Entregar o token por canal seguro (uma vez só).
2. **Endpoint de eventos**: o integrador cadastra a URL (Aplicação → Webhooks). O segredo de assinatura aparece **uma vez**. Conferir no CRM que a assinatura `X-Omnira-Signature` confere com o corpo cru e que o mesmo `event_id` repetido não duplica (o simulador faz isso: `crm-sim listen`).
   - Sem endpoint público ainda, o CRM pode **consultar** `GET /external/events?after=<seq>` (o diário é a fonte da verdade; o webhook é conveniência).
3. **Primeiro dia = leitura.** O CRM sincroniza (`/external/sync/checkpoint` → snapshot → eventos). Rodar o `headless-pilot-check.sh` de hora em hora; critérios de abortar na seção 4 do plano.
4. **Escrita** só depois de 24 h limpas e com o integrador na linha: nova aplicação com `conversation.claim` + `message.send` (e `transfer`/`finalize` só se o piloto precisar), `OMNIRA_HEADLESS_WRITE_ENABLED` já está ligada em produção; definir o **lease** (ex.: 600 s) pela tela ou pedindo ao operador.
5. **Eventos de serviço** (protocolos, ocorrências, tarefas, OS): só se o CRM os quiser. Escopo `service.read` na aplicação + assinatura dos tipos `service_*`; instalações com filtro de fila/linha **não** recebem fatos sem conversa (ocorrência, tarefa, registro do ERP).
6. **Fim das 24 h**: relatório com `headless-pilot-check.sh`, contagem de comandos (auditoria `external_app.command`), entregas de webhook `delivered` vs `dead`, e decisão: seguir, ajustar ou desfazer.

## Desfazer (em ordem)
1. Suspender a aplicação na tela (devolve as conversas ao roteador e corta o token na hora).
2. `OMNIRA_HEADLESS_WRITE_ENABLED=false` e recriar a api (se for desligar a escrita para todos).
3. Revogar a aplicação (definitivo) ou `OMNIRA_HEADLESS_ENABLED=false` + recriar api e worker.

## Receber webhook no próprio servidor (opcional, para provar o empurrão sem CRM de terceiros)
O guarda de rede só aceita endereço **público**. Para provar o webhook de ponta a ponta com o simulador, exponha uma rota nova do nginx do host e rode o `crm-sim listen` atrás dela:

```
# /etc/nginx/conf.d/00-omnira.conf  (dentro do server do omnira, ANTES de "location /")
location /pilot-hook/ {
    proxy_pass https://127.0.0.1:18443/;   # crm-sim listen (TLS) — só enquanto durar o teste
    proxy_ssl_verify off;
}
```
`SIM_WEBHOOK_SECRET=<segredo> crm-sim listen 127.0.0.1:18443 <cert.pem> <key.pem>` e cadastrar `https://omnira.devops.k3gsolutions.com.br/pilot-hook/omnira/events` como endpoint (porta 443, endereço público do próprio domínio). A rota só aceita o que tiver assinatura válida; remover o bloco do nginx depois do teste. **Não aplicado**: mexer no nginx do host é decisão do dono.
