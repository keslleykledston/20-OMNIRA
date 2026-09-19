# ADR 0010 — OIDC e sessão de navegador em cookie HttpOnly

**Status:** Accepted  
**Data:** 2026-09-19

## Contexto

O login mock emite chaves por processo, armazena o JWT no `localStorage` e não funciona com múltiplas réplicas. Isso bloqueia qualquer uso fora de LAB. A identidade autenticada precisa continuar separada da autorização de Tenant: o IdP prova quem é o Usuário; memberships persistidas determinam quais Tenants ele acessa.

## Decisão

- Usar OpenID Connect Authorization Code Flow com PKCE `S256`, `state` e `nonce`.
- Descobrir endpoints e chaves pelo documento `/.well-known/openid-configuration` e JWKS do issuer.
- Aceitar apenas ID Tokens `RS256`, validando assinatura, `kid`, issuer, audience, expiração e nonce.
- Reconciliar `sub` exclusivamente com `users.external_subject` de um Usuário ativo. Não criar Usuário, membership ou Tenant implicitamente no callback.
- Guardar o ID Token somente no cookie `omnira_session`, com `HttpOnly`, `SameSite=Lax`, `Path=/` e `Secure` obrigatório em produção. Tokens não retornam ao JavaScript nem ao `localStorage`.
- Manter Bearer JWT para clientes de API e o login mock apenas em desenvolvimento/teste. `OMNIRA_AUTH_MODE=mock` é recusado quando `OMNIRA_ENV=production`.
- A sessão fornece uma identidade. O `TenantContext` continua sendo construído por membership/grant no backend; `tenant_id` do token, navegador ou IdP não autoriza acesso.

Referências normativas: [OpenID Connect Core 1.0](https://openid.net/specs/openid-connect-core-1_0.html), [RFC 7636 — PKCE](https://www.rfc-editor.org/rfc/rfc7636.html) e [RFC 9700 — OAuth 2.0 Security BCP](https://www.rfc-editor.org/rfc/rfc9700.html).

## Consequências

- O IdP deve cadastrar exatamente a redirect URI configurada e emitir `aud` compatível com `OMNIRA_AUTH_AUDIENCE`.
- Usuários precisam ser previamente provisionados com `external_subject=sub` e membership ativa; identidade desconhecida falha fechada.
- O cookie dura no máximo até a expiração do ID Token. Renovação silenciosa/refresh token não entra nesta fase; o usuário autentica novamente após expirar.
- Logout local remove o cookie OMNIRA. Single logout no IdP pode ser acrescentado depois usando `end_session_endpoint`.
- Ambientes com TLS encerrado no proxy devem configurar `OMNIRA_AUTH_COOKIE_SECURE=true`.

## Alternativas

- JWT no `localStorage`: rejeitado por ampliar o impacto de XSS.
- Fluxo implícito: rejeitado; não oferece as garantias do Authorization Code + PKCE.
- Criar usuários/memberships automaticamente pelo token: rejeitado porque transforma claims externas em autoridade de Tenant.
- Sessões opacas em memória local: rejeitadas porque quebram múltiplas réplicas e reinícios. Uma store de sessão revogável pode ser adotada futuramente se houver requisito de revogação imediata.
