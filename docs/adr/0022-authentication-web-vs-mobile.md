# ADR-0022: Autenticação — Web (cookie de sessão) × Mobile (credencial por aparelho)

## Status
**Proposed (2026-10-07).** Documenta o estado real e recomenda um caminho. **Não há código** e a implementação (MOBILE.1) só começa com a aprovação do dono
nos pontos "Decisões do dono". O Web não muda.

## Contexto (verificado no código)
- **IdP:** Keycloak (OIDC). **Login Web:** Authorization Code + PKCE `S256` + `state` + `nonce`; o servidor troca o código (cliente confidencial), valida o
  ID Token (RS256, issuer, audiência `OMNIRA_AUTH_AUDIENCE`, expiração, nonce), reconcilia `issuer+sub` com uma identidade **já provisionada** (identidade
  desconhecida falha fechada) e cria uma **sessão opaca** em `auth_sessions` (id de 32 bytes aleatórios). Cookie `omnira_session`: `HttpOnly`, `SameSite=Lax`,
  `Secure` em produção. O ID Token não vai para o navegador nem é guardado.
- **Revogação:** `POST /auth/logout` marca `revoked_at`; `ResolveSession` recusa revogada/expirada; um JWT colocado no cookie é recusado (sem *fallback*). Não há *refresh token*: expirou, autentica de novo.
- **Bearer:** `WebMiddleware` aceita `Authorization: Bearer <JWT>` verificado por `OIDCAuthenticator` (mesmo issuer/audiência/identidade provisionada). Hoje é o ID Token do
  IdP; serve a ferramentas e a clientes de API.
- **Tenant e papéis:** independem do método: `AuthorizationMiddleware` resolve *membership* ativa e RLS reforça (ADR-0001/0010).
- **CORS/CSRF:** não há CORS (mesma origem via nginx); o cookie depende de `SameSite=Lax`. Cliente nativo com Bearer não usa cookie, logo não tem CSRF.
- **Lacunas para um app nativo:** (1) a audiência é **uma só** (`omnira-web`); um cliente OIDC do app teria outro `aud`; (2) o ID Token seria a credencial de API: não revogável
  por aparelho, de validade ditada pelo IdP, sem *refresh*, e a orientação OIDC/RFC 9700 desaconselha ID Token como credencial de aplicação; (3) não há conceito de aparelho
  (listar/revogar); (4) o SSE só reverifica *membership*, não a sessão (R-3).

## Opções
- **A. Sessão de aparelho emitida pelo OMNIRA (recomendada).** O app é cliente OIDC público (Code+PKCE no navegador do sistema, cliente `omnira-mobile` sem segredo) e entrega
  `code`+`code_verifier` ao OMNIRA (`POST /api/v1/auth/mobile/token`); o **servidor** troca com o Keycloak, valida como no `Callback` do Web, cria sessão em
  `auth_sessions` **com `device_id`** e devolve: token de acesso **opaco** de vida curta (padrão 15 min) + *refresh token* **rotativo** de uso único guardado só como *hash*, com
  detecção de reuso (reuso ⇒ revoga a família). Todas as rotas continuam usando o mesmo `WebMiddleware`; o app nunca vê o ID Token.
  Prós: revogável por aparelho no banco, mesma confiança do Web (id opaco), IdP pode ser trocado sem mudar o app, nenhuma mudança de `aud`. Contras: 3 endpoints + 1 tabela (`auth_devices`/colunas).
- **B. Bearer direto do IdP (já parcialmente funciona).** O app usa o token do Keycloak. Prós: nenhum código. Contras: audiência múltipla (mudança de segurança), sem revogação por aparelho
  nem listagem, refresh dependente do IdP, ID Token como credencial. **Rejeitada** como destino; aceitável só como atalho de desenvolvimento.
- **C. Senha direta (ROPC).** Rejeitada: o app veria a senha, quebra MFA/SSO.

## Decisão (proposta)
Opção A. Endpoints futuros (não criados):
`POST /api/v1/auth/mobile/token` · `POST /api/v1/auth/mobile/refresh` · `POST /api/v1/auth/mobile/logout` · `GET /api/v1/me/devices` · `DELETE /api/v1/me/devices/{device_id}`
(e visão de administrador para revogar aparelho de outro usuário com permissão própria). Regras:
- sem tenant no *token*: o tenant é endereçado pela URL e revalidado a cada requisição (membership);
- `refresh` rotativo, expiração **absoluta** (proposta 90 dias) e **deslizante** (30 dias); reuso revoga a família; logout/revogação invalidam imediatamente;
- o SSE reverifica a sessão no `recheck` (resolve R-3) e o evento de revogação fecha o fluxo;
- nenhum segredo, token ou *refresh* em log, URL ou fila; tabela guarda só *hash*;
- Web **não muda**: cookie HttpOnly continua sendo o único mecanismo do navegador; nada de `localStorage` para credencial sensível;
- `Authorization: Bearer` do IdP continua aceito como hoje (compatibilidade), sem ampliar audiências.

## Decisões do dono (bloqueiam o MOBILE.1)
1. Confirmar a Opção A (vs. aceitar B como destino).
2. TTLs: acesso 15 min; *refresh* 30 dias deslizante, 90 absoluto. Ajustar?
3. Quem pode revogar aparelho de outro usuário (proposta: `membership.manage`).
4. Um aparelho pode ter vários tenants? (proposta: sim, o mesmo usuário; a troca é no cliente e revalidada no servidor).
5. Criar o cliente `omnira-mobile` no Keycloak de produção (mudança de infraestrutura de identidade).

## Consequências
O Core ganha um segundo *front door* de sessão sem enfraquecer o Web; o custo é uma tabela de aparelhos e a rotação de *refresh*. Até a aprovação, **nada muda em produção**.
