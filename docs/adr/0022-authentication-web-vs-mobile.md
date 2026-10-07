# ADR-0022: Autenticação — Web (cookie de sessão) × Mobile (credencial por aparelho)

## Status
**Accepted (2026-10-07)** pelo dono, "conforme proposto" (Opção A e as decisões 1–5 abaixo, nos valores propostos). **Implementada no Core (MOBILE.1, 2026-10-07)**, atrás de `OMNIRA_AUTH_MOBILE_ENABLED` (desligada). Contrato do app em `docs/auth/MOBILE-AUTH.md`.
O Web não muda. O cliente `omnira-mobile` foi criado no Keycloak de produção em 2026-10-07 (público, PKCE S256, redirect `com.omnira.app:/oauth2redirect` provisório até o MOBILE.0 fixar o *application id*).

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

## Decisão (aprovada)
Opção A. Endpoints futuros (não criados):
`POST /api/v1/auth/mobile/token` · `POST /api/v1/auth/mobile/refresh` · `POST /api/v1/auth/mobile/logout` · `GET /api/v1/me/devices` · `DELETE /api/v1/me/devices/{device_id}`
(e visão de administrador para revogar aparelho de outro usuário com permissão própria). Regras:
- sem tenant no *token*: o tenant é endereçado pela URL e revalidado a cada requisição (membership);
- `refresh` rotativo, expiração **absoluta** (proposta 90 dias) e **deslizante** (30 dias); reuso revoga a família; logout/revogação invalidam imediatamente;
- o SSE reverifica a sessão no `recheck` (resolve R-3) e o evento de revogação fecha o fluxo;
- nenhum segredo, token ou *refresh* em log, URL ou fila; tabela guarda só *hash*;
- Web **não muda**: cookie HttpOnly continua sendo o único mecanismo do navegador; nada de `localStorage` para credencial sensível;
- `Authorization: Bearer` do IdP continua aceito como hoje (compatibilidade), sem ampliar audiências.

## Especificação de segurança obrigatória do MOBILE.1 (revisão independente, 2026-10-07)
Incorporada após a revisão do Codex; o MOBILE.1 não é aceito sem estes pontos e os testes correspondentes.

**Troca de código (`POST /auth/mobile/token`)**
- Cliente fixo `omnira-mobile` (público, sem segredo). O servidor envia ao Keycloak o `client_id` **fixo** e a `redirect_uri` **exata** de uma *allowlist* de configuração
  (esquema próprio/App Link/Universal Link); valores vindos do app só são aceitos se idênticos a um item da lista.
- PKCE `S256` obrigatório (`plain` recusado). `state` e `nonce` gerados no app; o servidor exige `nonce` na requisição e compara com a claim `nonce` do ID Token.
- Validação completa antes de criar sessão, como no Web: assinatura RS256, `iss`, `aud` contendo `omnira-mobile`, `azp == omnira-mobile`, `exp`, `nonce`, e identidade
  `issuer+sub` **já provisionada** (desconhecida falha fechada). Falha ⇒ 401 genérico, sem detalhar o motivo, sem registrar o código nem o *verifier*.
- *Rate limit* próprio por IP e por `device_id` no endpoint (R-2 precisa estar resolvido antes).

**Armazenamento (tabela `auth_devices` + colunas em `auth_sessions`; migration só no MOBILE.1)**
- `auth_devices(device_id uuid PK gerado pelo servidor, user_id, label, platform, created_at, last_seen_at, revoked_at)`; o `device_id` **nunca** é aceito do cliente na criação.
- Token de acesso e *refresh* são aleatórios de 32 bytes (CSPRNG); só o **SHA-256** é guardado (alta entropia dispensa *salt*/*pepper*); comparação em tempo constante;
  o valor em claro aparece uma única vez na resposta. Índice único no hash.
- `auth_sessions` ganha `device_id` e `family_id`; um aparelho tem no máximo uma família ativa por usuário; novo login no mesmo aparelho revoga a família anterior.
- Retenção: sessões/famílias revogadas ou expiradas guardadas 30 dias para auditoria e depois removidas; nunca guardar o valor em claro.

**Rotação do *refresh* (atômica)**
- Uma transação que **primeiro trava a família** (`SELECT ... FROM auth_families WHERE family_id=$f FOR UPDATE`, com `family_id` obtido pelo hash do *refresh*) e depois o token. A revogação (logout, usuário, administrador)
  trava a **mesma linha de família** antes de gravar `revoked_at`; assim refresh e revogação se serializam e nenhum sucessor nasce ativo depois de uma revogação confirmada.
- Rejeita (401) se: `revoked_at` da família; `used_at` do token já preenchido (**reuso ⇒ revoga a família inteira**); `expires_at` do token vencido (30 dias deslizante); ou `now() > absolute_expires_at` da
  família (90 dias, fixo na criação e **nunca** estendido pela rotação). Só então marca `used_at`, emite o sucessor (mesma família, `parent_id`) e renova o `expires_at` deslizante limitado ao absoluto.
- Duas requisições concorrentes com o mesmo *refresh*: a segunda espera o lock, vê `used_at` e dispara a revogação (comportamento seguro; o app refaz o login). Testes obrigatórios: corrida de N goroutines
  (exatamente 1 sucesso), refresh × revogação concorrentes (nenhum sucessor ativo após a revogação), token expirado, limite absoluto e reuso.
- Não há janela de tolerância na v1; o app persiste o sucessor antes de descartar o anterior.

**Identidade do aparelho**
- "Aparelho" = **instalação do app**, não hardware: não há prova criptográfica de hardware na v1. O `device_id` é emitido pelo servidor e guardado no Keychain/Keystore. Todo login cria um aparelho novo.
  O app pode enviar o `device_id` anterior (informativo); o servidor só revoga a família antiga se esse `device_id` pertencer ao **mesmo usuário já autenticado** (verificado depois da validação OIDC), nunca por confiança no valor.
  Aparelhos antigos sem uso expiram sozinhos (90 dias) e o usuário os vê/revoga em `GET/DELETE /me/devices`.
- Atestação (Play Integrity / App Attest) é melhoria futura (MOBILE.12) e não é requisito da v1.

**`state` e retorno do navegador**
- O app guarda `state` e `nonce` antes de abrir o navegador do sistema e **só envia o `code` ao servidor se o `state` retornado for igual (comparação em tempo constante)**; senão descarta. Teste negativo
  obrigatório no app (state trocado, ausente, repetido) e no servidor (nonce divergente ⇒ 401).

**Revogação e SSE**
- Logout ou revogação pelo próprio usuário (`DELETE /me/devices/{id}`) ⇒ `revoked_at` no aparelho, na família e nas sessões, imediatamente.
- **Escopo da revogação administrativa:** o aparelho é do usuário e vale para todos os seus tenants, e `membership.manage` é uma permissão **de um tenant**. Portanto um administrador só pode revogar o aparelho de outro usuário
  quando tem `membership.manage` em **todos** os tenants em que esse usuário é membro ativo; caso contrário, a ferramenta dele é remover/suspender a *membership* no seu tenant (já efetiva na requisição seguinte, ADR-0010).
  Revogação global de qualquer aparelho é só do próprio usuário ou de um papel de plataforma (futuro). Cada revogação administrativa gera auditoria.
- O SSE guarda o identificador da sessão que o abriu e, no `recheck` (30 s), chama `ResolveSession`; sessão revogada/expirada fecha o fluxo (resolve R-3 para Web e mobile).

## Decisões do dono (aprovadas em 2026-10-07, valores propostos)
1. Opção A confirmada (B fica só como atalho de desenvolvimento).
2. TTLs: acesso 15 min; *refresh* 30 dias deslizante, 90 absoluto.
3. Revogar aparelho de outro usuário: permissão `membership.manage`, restrita a quem a tem em todos os tenants do alvo (refinamento de segurança da revisão independente; o caso comum de um único tenant não muda).
4. Um aparelho pode ter vários tenants: sim, o mesmo usuário; a troca é no cliente e revalidada no servidor.
5. Criar o cliente `omnira-mobile` (público, sem segredo, Code+PKCE) no Keycloak de produção: autorizado, executado no MOBILE.1 (mudança de infraestrutura de identidade).

## Consequências
O Core ganha um segundo *front door* de sessão sem enfraquecer o Web; o custo é uma tabela de aparelhos e a rotação de *refresh*. Até o MOBILE.1, **nada muda em produção** por causa desta ADR.
