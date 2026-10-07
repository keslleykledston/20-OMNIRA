# ADR-0021: Android e iOS como novos clientes do OMNIRA Core

## Status
Accepted (2026-10-07) como **direção arquitetural**. A escolha do framework é **recomendada e deve ser validada no MOBILE.0** (condições abaixo).
Nada de aplicativo é construído por esta ADR.

## Contexto
O OMNIRA Core (Go, monólito modular, PostgreSQL com RLS, NATS) já concentra as regras: autorização por permissão, tenant por membership, roteamento,
IA, fluxos, antivírus de mídia. O Web (React 18 + TypeScript) é um cliente fino que só fala `/api/v1` (auditoria em `docs/architecture/mobile-readiness.md` §3).
Queremos aplicativos Android e iOS sem criar uma segunda implementação das regras.

## Decisão
1. **Clientes, não implementações.** Android/iOS consomem os mesmos contratos (`/api/v1`, SSE) que o Web. É proibido no app: decidir
   permissão (usar `GET /me/access` apenas para exibir), resolver tenant (vem de `GET /tenants` + membership), filtrar por segurança, falar com provedor
   de canal, IA, banco ou antivírus, ou guardar chave de provedor.
2. **Stack recomendada: React Native + Expo (TypeScript).** Motivos verificados no repositório: o Web já é React/TypeScript (equipe e tipos reaproveitáveis),
   contratos OpenAPI/AsyncAPI podem gerar tipos e cliente compartilhados, push FCM/APNs e atualização de binário são maduros. **Condições de validação no MOBILE.0**
   (se uma falhar, reavaliar antes de seguir): (a) SSE autenticado por cabeçalho `Authorization`: o próprio Web já faz isso com `fetch` + leitura de *stream* (não usa `EventSource`; `web/src/hooks/useRealtimeEvents.ts`, parser `readEvents` testado), mas o React Native precisa de *streaming* de `fetch` ou biblioteca equivalente — se instável, fallback = *refetch* ao receber push + *polling* curto em primeiro plano; o parser e a política de *backoff*/*refetch* devem ir para `packages/api-client`; (b) armazenamento em Keychain/Keystore; (c) captura de câmera/áudio e envio multipart.
3. **Estrutura futura do repositório** (a criar no MOBILE.0, não agora):
   ```
   apps/api  apps/worker                     (existentes, Go)
   web/                                      (existente; migrar para apps/web é opcional e não é pré-requisito)
   apps/mobile/                              (Expo)
   packages/api-contracts/   (gerado da OpenAPI/AsyncAPI: DTOs, enums, códigos de erro, tipos de evento)
   packages/api-client/      (cliente fino: envelope de erro, X-Request-ID, Idempotency-Key, paginação por cursor)
   ```
   Compartilhado: contratos, cliente fino, validações de formato. **Não** compartilhado: componentes de UI, estado de tela, abstrações de navegador,
   armazenamento de credencial (muda por plataforma).
4. **Papéis de domínio preservados** (ADR-0014, ADR-0018): Tenant = organização; Agente = usuário interno do tenant (nunca contato); Contato externo com
   `kind` (`other` = não classificado, não é cliente nem agente); Empresa = `customer_account` N:N com o contato; conversa interna ≠ atendimento.
   O app exibe e edita esses conceitos pelas mesmas rotas; não cria interpretações próprias.
5. **Flow Builder fica Web/Desktop.** O app não edita fluxos nem reimplementa o motor; uma mensagem enviada pelo app entra no mesmo ciclo
   (mensagem → motor/roteamento) porque o motor é do backend.

## Modelo de segurança do app (MOBILE.READINESS.13)
- Credencial (ADR-0022) apenas em **Keychain (iOS) / Keystore (Android)** através de uma abstração `SecureStore`; nunca em `AsyncStorage`, log, *deep link*, captura de tela de depuração.
- Token de acesso de vida curta; renovação por *refresh token* rotativo; **revogação** no servidor derruba o aparelho; **logout** revoga no servidor e apaga tudo local.
- **Aparelho perdido/roubado:** o administrador (ou o próprio usuário em outro aparelho) revoga o aparelho; o app deve cair em ≤ 30 s (SSE reverifica a sessão, ver R-3).
- **Biometria é conveniência local:** só destrava o acesso ao segredo já guardado; não autentica no servidor e não eleva permissão.
- **Troca de tenant:** explícita, limpa cache e *streams*, recarrega `me/access`. Nunca enviar `tenant_id` como se fosse autorização: ele é só o endereço; o servidor revalida a membership.
- **Rede:** TLS obrigatório; *certificate pinning* só se houver necessidade e com plano de rotação (não é requisito inicial); sem `http://` em produção.
- **Logs e relatórios de falha:** sem senha, token, cabeçalho `Authorization`, corpo de mensagem, telefone ou e-mail; usar `request_id` para correlacionar com o servidor.
- Nenhuma chave de IA, de canal (Meta/WAHA) ou do Keycloak confidencial no aparelho.

## Estratégia offline (MOBILE.READINESS.14) — não implementar agora
O servidor é sempre a fonte da verdade. Classificação do que pode ficar no aparelho:

| Classe | Exemplos | Regra |
|---|---|---|
| Pode cachear | lista de tenants, `me/access` (com validade curta), catálogos (filas, papéis, templates aprovados) | invalidar ao trocar tenant, ao logout e na revogação |
| Cache temporário | lista de conversas e últimas mensagens da conversa aberta | em memória/criptografado, expira rápido, apagado no logout/revogação; nunca vale como autorização |
| Sensível | corpo de mensagens, telefone, dados do contato, anexos | só em memória ou arquivo criptografado com chave do Keystore; sem *backup* do SO; sem pré-visualização em tela bloqueada |
| Nunca persistir | tokens fora do Keychain/Keystore, chaves de IA/canal, conteúdo de mídia não liberada pelo antivírus | — |

Ações feitas offline só serão suportadas com **`Idempotency-Key` gerada no aparelho** (já aceita em `POST …/messages` e na mudança de status de ticket) e
fila local ordenada; nenhuma mensagem pode ser duplicada em silêncio (reenvio com a mesma chave devolve `Idempotent-Replayed: true`). Cache não pode
dar acesso após logout/revogação: o apagamento é parte do fluxo de logout e da resposta 401.

## Observabilidade (MOBILE.READINESS.15)
`X-Request-ID` em toda resposta (cliente pode enviar o seu); envelope de erro opt-in com código estável (`Accept: application/vnd.omnira.v1+json`);
`event_id` nos eventos. O app envia `X-Request-ID` próprio por ação do usuário e o mostra em "Reportar problema". Erros de provedor continuam mapeados no servidor
(ex.: falha de entrega Meta). Pendente (fases futuras): códigos por domínio, métrica de latência por versão do app (`User-Agent` do cliente).

## Alternativas consideradas
- **Flutter / Kotlin+Swift nativos:** mais desempenho de UI, mas dobra ou triplica a equipe e não reaproveita tipos/cliente TypeScript. Reavaliar só se a condição (a) falhar.
- **PWA / Capacitor sobre o Web:** reaproveita a UI, mas push no iOS e câmera/áudio são limitados e a UI densa do Inbox é desktop-first.
- **Reimplementar regras no app (offline-first completo):** rejeitado: duplica autorização/roteamento e abre brecha de isolamento.

## Consequências
Positivas: um só lugar para regras e segurança; Web e app evoluem por contrato. Custos: exige gerar/manter contratos (MOBILE.0), upload de saída (R-4),
push (MOBILE.9) e credencial nativa (ADR-0022) antes de o app ser útil.
