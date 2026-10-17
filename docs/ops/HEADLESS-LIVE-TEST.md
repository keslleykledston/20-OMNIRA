# Teste real por WhatsApp — roteiro (gate externo da API headless)

Estado deste gate: **REAL-PROVIDER BLOCKED**. Nenhum envio por WhatsApp real foi feito pela suíte ou por quem implementou a branch. Este roteiro é o que falta para fechar o gate; é executado por uma pessoa, em um ambiente **isolado**, com o número de teste autorizado pelo dono: **+55 92 99188-2864** (destinatário). Nada aqui toca a produção, o banco real ou a linha real da empresa.

## O que este teste prova (e o que não prova)
Prova: uma mensagem enviada por `POST /api/v1/external/conversations/{id}/messages` (202 = enfileirada) **sai pelo worker de canal real**, chega ao telefone, e os status `sent → delivered → read` voltam ao CRM pelo journal (`message.status_changed.v1`); o Workspace não consegue falar por cima; o supervisor retoma; o CRM fecha o atendimento.
Não prova: nada de escala, falha de provedor sob carga, Meta Cloud (janela de 24 h/template) — para Meta, repetir com uma linha Meta de teste.

## Pré-requisitos
- Docker e docker compose na máquina do teste; a checkout desta branch (`feat/headless-crm-dual-operation`).
- **Um número REMETENTE de teste** (chip/WhatsApp próprio de teste) para conectar à linha do laboratório por QR. O +55 92 99188-2864 é só quem **recebe**.
- Nenhum segredo vai no chat: o QR e o token são manuseados nas telas do laboratório; o que for devolvido como evidência deve ter o token apagado.

## 1. Subir o laboratório isolado
```bash
scripts/headless-lab-stack.sh config   # valida o compose gerado (nada sobe)
scripts/headless-lab-stack.sh up       # projeto "omnira-hl": containers, rede, volumes e banco PRÓPRIOS; portas 4xxxx
scripts/headless-lab-stack.sh status
```
O laboratório liga `OMNIRA_HEADLESS_ENABLED` e `OMNIRA_HEADLESS_WRITE_ENABLED` **só nele**. Web: `http://127.0.0.1:43000`; API: `http://127.0.0.1:48080`. As migrations 137–141 rodam **no banco do laboratório**.

## 2. Preparar a empresa de laboratório
1. Entrar no web do laboratório (modo dev) com a empresa de demonstração que o `seed` cria (ou criar uma).
2. **Canais → Adicionar WhatsApp (WAHA)** e escanear o QR com o telefone **remetente de teste**. Esperar o status «working».
3. Do telefone **+55 92 99188-2864** mandar «oi» para a linha de teste. A conversa aparece em **Conversas**.

## 3. Instalar o CRM de referência
1. **Configurações → Integrações → Aplicações externas → Nova aplicação**: todas as permissões (leitura + escrita), sem filtro de fila/linha. Copiar o token (aparece uma vez).
2. Build do simulador: `go build -o /tmp/crm-sim ./apps/crm-sim`.
3. ```bash
   export OMNIRA_EXTERNAL_URL=http://127.0.0.1:48080/api/v1/external
   export OMNIRA_EXTERNAL_TOKEN='<o token>'
   /tmp/crm-sim me          # identidade e permissões
   /tmp/crm-sim bootstrap   # checkpoint -> snapshot -> eventos
   /tmp/crm-sim list open
   ```
   Webhooks: o endereço do endpoint precisa ser **https público** (a plataforma recusa rede privada de propósito). Sem um túnel https, siga só por consulta (`bootstrap` repetido lê o journal); com túnel, cadastre-o e rode `crm-sim listen`.

## 4. Roteiro (anote o horário e a saída de cada passo)
| # | Ação | Esperado |
|---|---|---|
| 1 | `crm-sim claim <conversa>` | 200; no web a conversa mostra «CRM» e **sem campo de resposta** |
| 2 | No web, tentar «Assumir» / responder | indisponível; via API 409 |
| 3 | `crm-sim send <conversa> <versão> "Teste OMNIRA headless"` | **202 queued** + `message_id` |
| 4 | Conferir o **telefone +55 92 99188-2864** | a mensagem chegou (confirmação humana) |
| 5 | `crm-sim show <conversa>` ou `bootstrap` | status da mensagem `sent`, depois `delivered`/`read` |
| 6 | Repetir o passo 3 com a **mesma** `SIM_KEY` (`export SIM_KEY=...`) | resposta `Idempotent-Replay`, **mesmo** `message_id`, **uma** mensagem no telefone |
| 7 | Responder do telefone; `crm-sim bootstrap` | `message.created.v1` (inbound) no journal |
| 8 | Supervisor no web: **Retomar atendimento** (motivo + confirmar) | conversa passa ao Workspace; `crm-sim send` com a versão antiga → 412/409 |
| 9 | Novo `claim` (outra conversa) → `crm-sim finalize` | 200; conversa «Finalizado» |
| 10 | Suspender a aplicação na tela | `crm-sim list` → 401; conversas devolvidas à fila |

## 5. Evidências a devolver (sem token)
Saída dos passos 1, 3, 5, 6, 8, 9, 10; foto/print do telefone nos passos 4 e 6; trecho do log do worker (`scripts/headless-lab-stack.sh logs worker | grep -i external`); e a consulta
`select status, count(*) from messages where sent_by_installation_id is not null group by 1;` no banco do laboratório.

## 6. Desmontar
```bash
scripts/headless-lab-stack.sh down   # apaga containers, rede e VOLUMES (banco do laboratório e a sessão do WhatsApp de teste)
```

Com a evidência dos passos 3–6 o gate externo muda de BLOCKED para «verificado com canal real (WAHA)». Sem ela, permanece BLOCKED.
