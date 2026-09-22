# IAM2C — Invitation delivery & e-mail confirmation (2026-09-22)

Fluxo: Tenant Admin → Convidar usuário → **e-mail entregue** (link seguro) → convidado abre → autentica via OIDC → convite validado → membership ativa → tela de confirmação. Sem senha local (OIDC continua a auth canônica).

## Estado antes (delta audit)
REAL: criar, token (32 B aleatórios, só sha256 no banco), expiração 72 h, revogar, uso único, aceite (e-mail case-insensitive, upsert de membership), continuação OIDC (`return_to` allowlist), isolamento por tenant/RLS, estados pending/accepted/revoked (+expired derivado), 20 testes Go.
PARTIAL: reenvio (só "novo convite substitui"), UI (sem "Enviado em"/Reenviar), entrega (porta `InvitationSender` + `Noop`).
ABSENT: provedor de e-mail (nada no repo: sem SMTP/SES/Resend/…), `email_verified`.

## O que foi feito
| Item | Como |
|---|---|
| Provedor | **SMTP com a stdlib** (`net/smtp`), sem dependência nova: `internal/tenancy/adapters/invitation_email.go`. Modos TLS `starttls` (padrão, exige STARTTLS) · `implicit` · `none` (só dev). Recusa enviar credenciais sem TLS. |
| Port | `InvitationSender.Send(ctx, InvitationMessage)` (mensagem estruturada). Implementações: SMTP, `Noop` (dev), fake nos testes. |
| Template | multipart text/plain + HTML (PT-BR), quoted-printable, Subject RFC 2047, `From`/`Reply-To` configuráveis, `Message-ID`, sem injeção de cabeçalho (CR/LF removidos, endereços validados). |
| Link | `OMNIRA_WEB_BASE_URL` (host do frontend que o navegador vê) + `/invite/<token>`. **Não** usa `OMNIRA_PUBLIC_BASE_URL` (URL server-to-server). |
| Config | `OMNIRA_SMTP_HOST/PORT/USERNAME/PASSWORD/FROM/REPLY_TO/TLS`, `OMNIRA_WEB_BASE_URL`. Boot falha se SMTP sem `FROM`/`WEB_BASE_URL` válida; em produção exige HTTPS e TLS ≠ none. |
| Entrega | Falha do provedor → **502**; o convite fica `pending` com `sent_at` nulo (visível na lista, reenviável). `sent_at` (migration 000037) = última entrega bem-sucedida. Log só com o id do convite — nunca token, link ou corpo do erro. |
| Reenviar | `POST /tenants/{id}/team/invitations/{id}/resend` (`membership.manage`): reemite o token (link antigo morre), renova 72 h, reenvia. Só `pending` (inclusive vencido); aceito/revogado → 409; outro tenant → 404; sem entrega configurada → 503. Auditoria `membership.invitation.resent`. |
| `email_verified` | Lido do ID token (bool ou `"true"`, estrito), gravado em `user_identities.email_verified` a cada login (migration 000037). **Aceite e status exigem** identidade do usuário com `email_verified=true` e o mesmo e-mail do convite (status `email_unverified` / 403). Em dev/lab (dev auth ativo, sem IdP) a regra não se aplica. Sem account linking por e-mail; identidade canônica segue `(issuer, subject)`. |
| Defeito corrigido | O aceite fazia upsert do `role_id` e podia **rebaixar um membro ativo** (até o último admin). Agora o papel de quem já está ativo é preservado; convidar um membro ativo → 409 (mudar papel = `PATCH /team`, com o invariante do último admin). |
| UI | Equipe e acesso → Convites: colunas E-mail · Função · Status · Expira · **Enviado em** ("Não entregue" quando não chegou) · Enviado por; ação **Reenviar convite** (pendente/vencido, só com entrega configurada) e Revogar; aviso "Convite enviado/reenviado para …"; erro 502 orienta a reenviar; página de aceite com estado `email_unverified`. |
| Dev | Serviço `mailpit` no compose (profile `dev`, UI :8025); variáveis comentadas no `.env.example`. |

## Testes
Go (Postgres real): entrega com link/host corretos e só hash persistido, falha → 502 + pending/sem `sent_at`, reenvio (token novo, antigo 404, vencido revivido, single-use, regras 409/403/404/503, falha no reenvio), convidar membro ativo, aceite não rebaixa, `email_verified` (sem identidade / não verificado / outro e-mail / verificado, status `email_unverified`), claim estrito, persistência do claim, SMTP (mensagem multipart, injeção de cabeçalho, credenciais sem TLS, STARTTLS obrigatório, erro sem token), config SMTP. Vitest +7 (TeamPage, AcceptInvitePage). Playwright `invitation-email.spec.ts`: convite pela UI → e-mail capturado por um sink SMTP com link no host do frontend → "Enviado em" → Reenviar → link antigo morto/novo vivo.

## Pendências / riscos
- **Aplicar a migration 000037 no `omnira_dev`** (banco do dono; requer OK). Sem ela a API nova não sobe contra esse banco.
- **Depende do IdP:** Keycloak/Google enviam `email_verified`; se o IdP de produção não enviar, o aceite fica bloqueado (`email_unverified`) — auditar o IdP antes de produção. Realm de lab: usuários precisam de e-mail verificado.
- Endpoints de convites ainda **não estão no OpenAPI** (débito herdado do IAM2B).
- Fila/retry de e-mail (envio é síncrono no request; falha vira 502 + reenviar). Tratamento de bounce fora de escopo.
- Handlers legados sem rota (`ListMemberships`/`CreateMembership`/`RevokeMembership`) continuam no código (candidatos a remoção).
