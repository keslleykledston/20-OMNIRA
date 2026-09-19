# Aba "Integrações" — projeto

> Estado: **PROPOSTA (design)**. Nada aqui foi implementado além do que está marcado **[existe]**.
> Complementa `INTEGRATIONS.md` (princípio anti-corrupção, porta canônica ERP, contrato de canais) e
> `docs/ops/RUNBOOK-INBOX-WAHA.md`. Hoje existe a página `/channels` ("Canais") só para WAHA; esta aba a **substitui e generaliza**.

## 1. Objetivo
Uma única tela, no web, onde o administrador do tenant **conecta plataformas** sem editar `.env` nem SQL:
- plataformas de **sessão por QR** (WhatsApp não oficial/WAHA): a tela **gera e exibe o QR**;
- plataformas de **credenciais** (WhatsApp Meta Cloud, futuras): a tela **pede os parâmetros** e **exibe os parâmetros que o usuário deve copiar para o painel da plataforma** (URL de callback, verify token, eventos);
- plataformas de **API de terceiros** (ERPs IXC/SGP/Hubsoft): formulário de credenciais + teste.
Deve mostrar o **estado real** de cada conexão, permitir **testar, reconectar, rotacionar segredo e revogar**, e ser **extensível**: adicionar uma plataforma não pode exigir nova tela.

## 2. Princípios (herdados do projeto)
1. **Tenant só do JWT+membership**; permissão `channel.manage` (admin) para tudo que muda estado; leitura de status pode ter permissão própria (`channel.read`) no futuro.
2. **Segredos são write-only**: nunca voltam pela API nem aparecem no HTML/logs/URLs; são cifrados (AES-256-GCM, `CredentialStore`); a UI só mostra máscara (`••••abcd`) e data da última rotação.
3. **Provedor não oficial exige aceite de risco** registrado (usuário+hora) — já **[existe]** para WAHA.
4. **Anti-corrupção**: o core não conhece campos de plataforma. A tela é dirigida por um **descritor declarativo** servido pelo backend.
5. **Estado vem do backend** (nunca inferido no cliente); erro do provedor nunca é mascarado como sucesso.
6. Nada de "sucesso fantasma": conexão só fica **Conectado** depois de um teste/handshake real.

## 3. Catálogo de plataformas (descritor declarativo)
O backend expõe `GET /api/v1/tenants/{tid}/channels/providers` com um descritor por plataforma. A UI renderiza o assistente **a partir do descritor**.

```jsonc
{
  "id": "waha",                       // estável; = ChannelConnection.provider
  "name": "WhatsApp (não oficial)",
  "channel": "whatsapp",
  "kind": "unofficial",               // official | unofficial
  "connect_method": "qr_session",     // qr_session | credentials | oauth_redirect
  "risk_notice": "…pode causar banimento…",       // exige aceite se kind=unofficial
  "capabilities": ["text","delivery_status","qr_pairing"],
  "enabled": true,                    // false quando o servidor não está configurado (ex.: sem WAHA)
  "unavailable_reason": null,         // ex.: "OMNIRA_WAHA_ENABLED=false"
  "inputs": [],                       // campos que o USUÁRIO informa (vazio no WAHA)
  "displays": []                      // parâmetros que o SISTEMA mostra para o usuário copiar
}
```
Campos de `inputs[]`: `key`, `label`, `type` (`text|secret|select|phone`), `required`, `pattern`, `help`, `example`, `secret` (write-only).
Campos de `displays[]`: `key`, `label`, `value_template`, `copyable`, `sensitive` (mostrar uma vez), `help`.

### 3.1 O que cada plataforma pede e mostra
| Plataforma | Método | Usuário **informa** (write-only) | Sistema **mostra** (copiar) | Sistema **gera** |
|---|---|---|---|---|
| **WhatsApp WAHA** [existe] | `qr_session` | — (só o aceite de risco) | **QR** (renova a cada ~8 s), número pareado, status da sessão | chave HMAC do webhook (cifrada, **nunca exibida**), sessão `omnira_<id>`, webhook registrado no WAHA |
| **WhatsApp Meta Cloud** [provider existe; config por tenant **não** existe] | `credentials` | `phone_number_id`, `waba_id`, `access_token` (secret), `app_secret` (secret) | **Callback URL** `https://<host>/webhooks/v1/whatsapp/meta/<connection_id>`, **Verify Token** (mostrar 1×, rotacionável), eventos a assinar (`messages`), versão da Graph API | verify token, id de conexão |
| **ERP (IXC/SGP/Hubsoft)** [porta canônica definida; sem adapter na UI] | `credentials` | URL base, usuário/token (secret), opcionais por ERP | resultado do **teste** (assinante de exemplo/latência), IPs de saída a liberar no firewall do ERP | — |
| **Futuras** (Instagram, Telegram, e-mail) | `credentials`/`oauth_redirect` | por descritor | por descritor | por descritor |

> Meta hoje usa **`OMNIRA_META_VERIFY_TOKEN`/`OMNIRA_META_APP_SECRET` globais** e resolve o tenant por `phone_number_id`. O design move isso para **credenciais por conexão** (rota com `connection_id`, segredos cifrados por tenant) — mudança de backend em §7.

## 4. UX

### 4.1 Estrutura
Item de menu **Integrações** (substitui "Canais") → rota `/integrations`.
```
┌─ Integrações ───────────────────────────────────────────────  [+ Adicionar integração] ─┐
│ Filtros: [Todas ▾] [Status ▾]                                                            │
│ ┌─ WhatsApp · WAHA (não oficial) ────────────── ● Conectado ─┐ ┌─ WhatsApp · Meta Cloud ─ ○ Pendente ┐
│ │ +55 11 98888-7777 · pareado 19/09 14:02                     │ │ Aguardando verificação do webhook    │
│ │ Sessão: working · última msg há 2 min                       │ │ Passo 2 de 3                         │
│ │ [Testar] [Reconectar] [Parar] [Revogar…]                    │ │ [Continuar]                          │
│ └─────────────────────────────────────────────────────────────┘ └──────────────────────────────────────┘
│ (vazio) "Nenhuma integração ainda. Conecte seu primeiro canal." [+ Adicionar integração]              │
└──────────────────────────────────────────────────────────────────────────────────────────────────────┘
```
Cartão: logo/nome, tipo (badge **não oficial** em destaque), **status** (cor + texto, nunca só cor), identificador (número/WABA), horários, ações contextuais, mensagem de erro **acionável**.

### 4.2 Assistente "Adicionar integração"
1. **Escolher plataforma** (grade do catálogo; itens `enabled:false` aparecem desabilitados com o motivo).
2. **Passos ditados pelo descritor**, com indicador de progresso e "Voltar".

**a) QR (WAHA)**
```
 Passo 1  Aviso de risco  ☐ Entendo e aceito (registrado com meu usuário e hora)  [Continuar]
 Passo 2  Parear
   ┌──────────────┐   1. No celular abra o WhatsApp
   │   [ QR ]     │   2. Configurações → Aparelhos conectados → Conectar aparelho
   │  válido 0:14 │   3. Aponte a câmera para este código
   └──────────────┘   O código renova sozinho. [Gerar novo QR]
   Estado: Aguardando leitura… → Conectando… → ✔ Conectado como +55 11 98888-7777
```
- QR como `<img>` `data:` (base64), `Cache-Control: no-store`, **contagem regressiva** e renovação automática; ao expirar sem leitura mostra "Gerar novo".
- Polling do status (2 s) **somente** enquanto o assistente está aberto; ao conectar, fecha e o cartão aparece **Conectado**. (Fase I3 troca o polling por SSE.)
- Alternativa **"Parear por código"** (número do telefone → código de 8 caracteres) quando o WAHA suportar — reduz a dependência de duas telas.
- Acessibilidade: instruções em texto, `aria-live` para mudanças de estado, contraste do QR (fundo branco, margem).

**b) Credenciais (Meta Cloud)**
```
 Passo 1  Informe os dados do seu app        (link "onde encontro isso?" → guia curto)
          Phone Number ID [__________]   WABA ID [__________]
          Access Token    [••••••••••]   App Secret [••••••••••]     [Validar e continuar]
 Passo 2  Configure o webhook no painel da Meta        (mostrado após validar)
          Callback URL  https://…/webhooks/v1/whatsapp/meta/3f9c…   [Copiar]
          Verify Token  k2Hx…9pQ  (mostrado uma vez) [Copiar]  [Rotacionar]
          Assinar o campo:  messages
 Passo 3  Verificar   [Verificar webhook]  → ✔ Meta confirmou o callback  → ✔ Número ativo
```
- "Validar" chama a Graph API **no servidor** (token nunca passa pelo navegador de volta) e confirma que o `phone_number_id` pertence à WABA.
- Segredos: campo `type=password`, `autocomplete=off`, sem persistir em `localStorage`/estado após envio; ao editar, mostra `••••` e só envia se o usuário digitar novo valor.
- **Verify token** é exibido **uma vez** (com opção de rotacionar); depois só a máscara.

**c) ERP**: formulário (URL base, credenciais) → **Testar conexão** (chama `find_subscriber` de teste) → mostra latência/erro; lista IPs de saída.

### 4.3 Estados e ações
`pending → connecting → connected ⇄ degraded → disconnected → (reconectar) → connected`; `failed` (erro terminal com causa) ; `revoked` (irreversível).
| Ação | Quando | Efeito | Confirmação |
|---|---|---|---|
| Testar | conectado/degradado | teste ativo (health do provedor) | não |
| Reconectar | desconectado/falho | reinicia sessão/QR ou revalida credenciais | não |
| Parar | conectado (sessão) | encerra sessão, mantém conexão | sim (curta) |
| Rotacionar segredo | credenciais | gera novo verify token / pede novo token | sim |
| Revogar | qualquer | apaga segredos, encerra sessão, **irreversível**, conversas históricas preservadas | **sim, digitar o nome** |

### 4.4 Erros (mensagens acionáveis, sem vazar internals)
| Situação | Mensagem |
|---|---|
| 403 | "Somente administradores do tenant gerenciam integrações." |
| Gateway WAHA fora (502) | "O gateway do WhatsApp não respondeu. Tente novamente; se persistir, contate o suporte." |
| Servidor sem URL pública (503) | "Integração indisponível: o servidor não tem a URL pública configurada." |
| Meta: token inválido | "A Meta recusou o token. Gere um token permanente (System User) com `whatsapp_business_messaging`." |
| Meta: webhook não verificado | "A Meta ainda não confirmou o callback. Confira URL e Verify Token e clique em Verificar." |
| QR expirou | "O código expirou. [Gerar novo QR]" |

### 4.5 Requisitos transversais de UI
Responsivo (assistente em tela cheia no mobile), teclado/leitor de tela, i18n pt-BR (padrão) e en, estado vazio/carregando/erro em toda lista, **nunca** bloquear a tela inteira por erro de uma conexão, sem `alert()`, textos de risco visíveis (não escondidos em tooltip).

## 5. Modelo e estados no backend (proposto)
Reutiliza `ChannelConnection` **[existe]** e acrescenta:
- `display_name` (rótulo do operador), `last_verified_at`, `last_error_code` (classe, sem texto do provedor), `webhook_verified_at` (Meta).
- Credenciais por conexão em `channel_credentials` **[existe]** (chaves: `access_token`, `app_secret`, `verify_token`, `webhook_hmac_key`…), com `updated_at` para "rotacionado em".
- Máquina de estados única (a de `ConnectionStatus` **[existe]**: pending/active/degraded/disconnected/failed/revoked) + `session_status` (WAHA) como detalhe.

## 6. API (proposta; extensão do contrato OpenAPI)
Prefixo `/api/v1/tenants/{tenant_id}/channels`. Todas: JWT + `channel.manage`; erros como hoje (texto) até haver Problem Details.
| Método e rota | Função |
|---|---|
| `GET /providers` | catálogo/descritores (§3); `enabled`/`unavailable_reason` refletem a configuração do servidor |
| `GET /connections` | lista (todas as plataformas), sem segredos |
| `POST /connections` | cria `{provider, display_name, inputs{…}, risk_acknowledged?}`; valida contra o descritor; retorna a conexão + `displays` (ex.: callback URL + verify token **1×**) |
| `GET /connections/{id}` | estado vivo (persiste o status derivado) + `displays` não sensíveis |
| `POST /connections/{id}/session/start\|stop` | sessões por QR (WAHA) **[existe em /waha/connections]** |
| `GET /connections/{id}/qr` | QR (só em `needs_qr`; 409 antes) **[existe]** |
| `POST /connections/{id}/test` | teste ativo; retorna `{ok, latency_ms, error_class?}` (rate limit) |
| `POST /connections/{id}/credentials` | rotaciona/atualiza segredos (write-only) |
| `POST /connections/{id}/verify-webhook` | (Meta) confere que a plataforma já chamou o callback |
| `DELETE /connections/{id}` | revoga (apaga segredos, encerra sessão) |
Compatibilidade: `/channels/waha/connections/*` **[existe]** vira alias deprecado até a UI migrar. Auditoria: `channel.connection_created|credentials_rotated|session_started|session_stopped|revoked|tested`.

## 7. Mudanças de backend necessárias
1. **Registro de provedores com descritor** (`ports.ProviderDescriptor`): cada adapter declara `connect_method`, `inputs`, `displays`, `capabilities`; `GET /providers` só lê o registro (+ config do servidor).
2. **Serviço genérico de conexões** (hoje `WahaConnectionService` é específico): valida `inputs` pelo descritor, grava credenciais cifradas, aciona o adapter (`Connect/Test/Disconnect`).
3. **Meta por conexão**: rota `POST/GET /webhooks/v1/whatsapp/meta/{connection_id}` (como o WAHA), `verify_token` e `app_secret` por conexão (fim dos globais `OMNIRA_META_*`); verificação de assinatura `X-Hub-Signature-256` com o `app_secret` da conexão; resolução do tenant **pela conexão do path** (nunca pelo payload).
4. **Exposição pública controlada**: hoje o nginx **bloqueia** `/webhooks` (correto para WAHA, interno). Para Meta é preciso um `location /webhooks/v1/whatsapp/meta/` dedicado, com **rate limit**, limite de corpo e sem `/internal`.
5. **Estado em tempo real**: evento `channel_connection_updated` (mesmo caminho NOTIFY→NATS→SSE do inbox) para trocar o polling; job de reconciliação periódica (status vivo do provedor → `ChannelConnection.status`).
6. **Teste ativo** por adapter (`Test(ctx, conn) Result`) com timeout e classificação de erro (`authentication`, `rate_limited`, `unavailable`, `misconfigured`).
7. **Cotas**: máximo de conexões por tenant/plataforma; limite de QR/sessões simultâneas.

## 8. Frontend (proposto)
```
web/src/pages/IntegrationsPage.tsx            lista + filtros + "Adicionar"
web/src/features/integrations/
  api.ts                 cliente tipado (gerado do OpenAPI quando existir)
  useProviders.ts        catálogo (react-query, cache longo)
  useConnections.ts      lista + mutações
  useConnectionLive.ts   polling → SSE (channel_connection_updated)
  ConnectionCard.tsx     estado, ações, erro acionável
  AddIntegrationWizard.tsx   passos dirigidos pelo descritor
  steps/RiskStep.tsx  QrStep.tsx  CredentialsStep.tsx  WebhookInfoStep.tsx  VerifyStep.tsx
  components/QrImage.tsx (contagem, renovação)  CopyField.tsx (mostrar 1×, copiar)  SecretInput.tsx
```
Regras: nenhum componente conhece uma plataforma específica; tudo vem do descritor. Segredos só em estado local do passo e descartados no envio. Tokens do usuário como hoje (sessão) — **sem** reenviar segredos de plataforma ao navegador.

## 9. Segurança e privacidade
- Autorização por permissão; RLS de escrita já restringe `channel_connections/credentials` a admin.
- **Segredos**: cifrados em repouso; nunca em resposta/log/URL/telemetria; máscara com últimos 4 no máximo; rotação auditada; "mostrar 1×" para tokens gerados.
- **Anti-SSRF** em qualquer campo de URL (ERP): só `https`, bloqueio de IPs privados/loopback/link-local, timeout, sem seguir redirects para hosts diferentes.
- **Webhook**: HMAC (WAHA: SHA-512; Meta: SHA-256 `X-Hub-Signature-256`), idempotência por id de evento, tenant sempre pela conexão do path, resposta 401 só para assinatura inválida e 503 para falha nossa (para o provedor reenviar).
- **Rate limit** em `test`, `verify-webhook`, criação e login.
- **CSP** estrita já ativa (`web/security-headers.conf`); QR e imagens só `data:`/`self`.
- Aceite de risco imutável (quem/quando/versão do texto).
- LGPD: número pareado é dado pessoal — mostrar só ao admin do tenant; revogar apaga segredos, mantém histórico conforme política de retenção.

## 10. Observabilidade
Métricas (baixa cardinalidade): `channel_connections{provider,status}` (gauge), `channel_operation_total{provider,operation,status}` **[existe]**, `channel_webhook_total`/`_invalid_total` **[existe]**, `integration_test_total{provider,result}`, `qr_generated_total`, tempo até conectar (`histogram`). Logs estruturados com `connection_id` (nunca segredos). Alerta: conexão `active` sem mensagens/ack há X h; webhook sem entregas; falha de reconciliação.

## 11. Testes (critérios de aceite)
- **Unit (web)**: renderização do assistente a partir de descritores (QR, credenciais, ERP); máscara/1× de segredos; expiração e renovação do QR; erros mapeados.
- **Integração (Go, Postgres real como `omnira_app`)**: RBAC (agent/supervisor = 403), isolamento entre tenants (404 sem oráculo), **segredos nunca retornam** (varredura das respostas), aceite de risco obrigatório, rotação, revogação apaga segredos, cotas.
- **Webhook Meta**: challenge, assinatura, tenant pelo path, idempotência (espelhar `internal/e2e/vertical_test.go`).
- **E2E navegador (`scripts/e2e-inbox.sh` / clean-room)**: criar WAHA → QR real visível → parar; criar Meta com Graph API **fake** → callback/verify token exibidos → verificação → ativo; erro de token → mensagem acionável.
- **Contrato**: OpenAPI atualizado + teste de drift (**[existe]**).
- **Manual (P7)**: pareamento real com telefone; Meta com número de teste.

## 12. Fases sugeridas
| Fase | Entrega | Depende de |
|---|---|---|
| I0 | Registro de provedores + `GET /providers`; serviço genérico de conexões; alias do WAHA | — |
| I1 | Aba `/integrations` (lista + assistente QR/WAHA) substituindo `/channels` | I0 |
| I2 | Meta por conexão (credenciais, callback/verify token, exposição nginx com rate limit) + assistente de credenciais | I0 |
| I3 | Estado em tempo real (SSE) + reconciliação + métricas/alertas | I1 |
| I4 | ERPs (IXC/SGP/Hubsoft) na mesma aba (credenciais + teste + IPs de saída) | I0 |
| I5 | "Parear por código", reconexão guiada, cotas e telemetria de funil | I1 |

## 13. Riscos e decisões em aberto
1. **Meta em produção exige exposição pública do webhook** (hoje bloqueada): decidir domínio/rate limit/WAF antes de I2.
2. **WAHA**: QR depende do engine (GOWS); "parear por código" precisa de confirmação de suporte na versão fixada.
3. **Multi-conexão por tenant**: definir política (um número por conexão; roteamento por conexão na fila) e cotas.
4. **IdP real** (OIDC) é pré-requisito de produção para qualquer aba administrativa.
5. **Migração dos segredos globais da Meta** (`OMNIRA_META_*`) para por-conexão: plano de compatibilidade (fallback global só em LAB).
6. Definir se `verify token` pode ser reexibido (recomendado: **não**, só rotacionar).
