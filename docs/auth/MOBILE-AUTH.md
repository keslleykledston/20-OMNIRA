# Autenticação de apps nativos (ADR-0022, MOBILE.1)

Contrato para o futuro app Android/iOS. **O app não existe ainda**; isto é o que o Core já oferece. Desligado por padrão (`OMNIRA_AUTH_MOBILE_ENABLED=false`): com a flag
desligada as rotas `/auth/mobile/*` e `/me/devices` respondem 404 e nada muda para o Web.

## Visão geral
- O app é um cliente OIDC **público** (`omnira-mobile`, Code + PKCE `S256`, sem segredo) no mesmo Keycloak do Web. Entrada no navegador do sistema
  (`ASWebAuthenticationSession` / Custom Tabs), nunca em WebView.
- O **servidor** troca o `code` com o Keycloak e valida o ID Token (assinatura RS256, `iss`, `aud` = `omnira-mobile`, `azp` = `omnira-mobile`, expiração, `nonce`,
  identidade provisionada). O ID Token **não** chega ao app. O app recebe uma **sessão de aparelho** opaca:
  - `access_token` (`omn_at_…`): 15 minutos, vai em `Authorization: Bearer` em **todas** as rotas já existentes;
  - `refresh_token` (`omn_rt_…`): uso único, 30 dias deslizante, **90 dias absolutos** desde o login;
  - `device_id`: a instalação (não o hardware).
- Só *digests* SHA-256 ficam no banco. Os valores em claro aparecem uma única vez, na resposta que os emite (`Cache-Control: no-store`).
- Tenant **não** vai no token: continua na URL e é revalidado (membership + RLS) a cada requisição. Um aparelho serve a vários tenants do mesmo usuário.

## Fluxo
1. App gera `state`, `nonce` (16–128 chars `[A-Za-z0-9-._~]`) e `code_verifier` (43–128 chars) e abre o navegador em
   `<authorization_endpoint>?response_type=code&client_id=omnira-mobile&redirect_uri=<uri>&scope=openid email profile&state=…&nonce=…&code_challenge=…&code_challenge_method=S256`.
2. Ao voltar pelo redirect, o app **compara o `state` em tempo constante** e só então envia o `code`:
   `POST /api/v1/auth/mobile/token` `{code, code_verifier, redirect_uri, nonce, device_label?, platform?, previous_device_id?}`.
   `redirect_uri` precisa estar **exatamente** em `OMNIRA_AUTH_MOBILE_REDIRECT_URIS`.
3. Resposta: `{token_type, access_token, access_expires_at, expires_in, refresh_token, refresh_expires_at, device_id}`.
4. Antes de expirar (ou ao receber 401), `POST /api/v1/auth/mobile/refresh` `{refresh_token}` devolve **um par novo**; o anterior morre. O app deve **gravar o par novo
   antes de descartar o antigo**. Reapresentar um refresh já usado revoga o aparelho inteiro (o app refaz o login). Toda falha é o mesmo `401`.
5. `POST /api/v1/auth/mobile/logout` (Bearer, ou `{refresh_token}` se o acesso já expirou) → `204`, efeito imediato (inclusive fecha o SSE aberto).
6. `GET /api/v1/me/devices` lista as instalações do usuário; `DELETE /api/v1/me/devices/{device_id}` revoga uma (de outro usuário responde `404`).

## Armazenamento no aparelho (regras duras)
Tokens só no Keychain (iOS) / Keystore (Android). Nunca em log, URL, preferências em claro, analytics ou backup. Nada de WebView para login.

## Limites e proteção
- 20 trocas de código e 60 renovações/logouts por minuto por origem; até 20 aparelhos vivos por usuário (o menos usado é revogado).
- Cotas por tenant (6000/min) e por usuário (1200/min) valem **depois** da membership verificada (`OMNIRA_RATELIMIT_*_PER_MIN`).
- O SSE (`/inbox/events`) reverifica a sessão (cookie ou aparelho) a cada 30 s e fecha se foi revogada.

## Operação
Ligar: `OMNIRA_AUTH_MODE=oidc`, `OMNIRA_AUTH_MOBILE_ENABLED=true`, `OMNIRA_AUTH_MOBILE_REDIRECT_URIS=<uri1>,<uri2>` (sem curinga; `http` só em loopback) e o cliente
`omnira-mobile` no Keycloak (público, PKCE S256, sem *direct grants*, sem *implicit*). Revogar tudo de um usuário: `UPDATE auth_devices SET revoked_at=now()` + famílias
(ou a API de aparelhos). Migration `000090` (reversível: `down` remove as 4 tabelas).
