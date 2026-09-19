# ADR-0009: Criptografia de credenciais de canal em repouso

## Status
Accepted

## Contexto

D3.1 (WhatsApp Provider Layer) precisa persistir credenciais de canal
(access token da Meta Cloud API, e futuramente segredos de outros
providers) de forma que:

- nunca apareçam em texto puro em `NATS`, logs, resposta de API ou volte
  ao frontend;
- `ChannelConnection.SecretRef` seja uma referência opaca, nunca o
  segredo em si (já definido em D2);
- o mecanismo seja trocável sem reescrever o domínio/aplicação, caso o
  projeto evolua para um KMS externo (Vault, AWS KMS, GCP KMS) — decisão
  de infraestrutura tipicamente adiada até o tier Enterprise/Dedicated
  Database existir de fato.

Não existe ADR ou decisão prévia sobre este mecanismo no projeto.

## Decisão

Criptografia simétrica AES-256-GCM em nível de aplicação, com uma chave
mestra de 32 bytes vinda de variável de ambiente
(`OMNIRA_CREDENTIALS_KEY`, base64), abstraída atrás de uma interface
(`internal/channels/ports.CredentialCipher`) para que trocar a
implementação por um KMS externo no futuro não exija mudar
`CredentialStore`, `ChannelService` ou qualquer adapter de provider.

`OMNIRA_CREDENTIALS_KEY` usa Base64 padrão e deve decodificar exatamente para
32 bytes aleatórios. Ausência, Base64 inválido ou tamanho incorreto impede o
boot da API/worker; a chave nunca é registrada em logs, métricas, traces ou
payloads.

Cada credencial é armazenada como `(nonce, ciphertext)` numa tabela
tenant-owned (`channel_credentials`, RLS + FORCE obrigatórios, ver
`internal/platform/db`), nunca em texto puro nem em variável de ambiente
por tenant. O nonce é gerado por chamada de `Encrypt` (nunca reaproveitado
entre credenciais).

`ChannelConnection.SecretRef` guarda o UUID da linha em
`channel_credentials`, nunca o ciphertext nem a chave.

## Consequências

- Sem dependência de infraestrutura externa no MVP (Docker-first,
  Compose-only continua válido — nenhum serviço de KMS adicional).
- Uma única chave mestra por ambiente é um ponto único de falha
  aceitável para o estágio atual (Shared Strong Isolation); revisitar
  quando o tier Dedicated Database existir (chave por tenant, ou KMS
  externo com envelope encryption).
- Rotação de chave mestra exige reescrever todas as linhas de
  `channel_credentials` (não implementado nesta wave — registrar como
  dívida quando o volume de credenciais justificar automatizar).
- `OMNIRA_CREDENTIALS_KEY` ausente/inválida deve falhar o boot do
  worker/API que precisar dela, nunca operar em modo "sem criptografia"
  silenciosamente.

## Alternativas consideradas

- **pgcrypto (`pgp_sym_encrypt`)**: manteria a chave "mais perto" do
  banco, mas move lógica de segurança para dentro de SQL, dificulta
  testar a criptografia isoladamente do Postgres, e ainda exige gerenciar
  a mesma chave mestra em algum lugar (não elimina o problema, só move
  onde ele vive). Rejeitado para não acoplar a camada de aplicação ao
  dialeto de uma extensão específica do Postgres.
- **KMS externo desde já (Vault/AWS KMS)**: adiado — introduz
  dependência de infraestrutura antes de haver um caso de uso (tier
  Dedicated Database) que justifique o custo operacional. A interface
  `CredentialCipher` é o ponto de extensão para isso quando chegar a
  hora, sem exigir ADR de revogação.

## Telemetria

O CredentialStore registra somente contadores de resolução com status de
sucesso/erro. Não usa `tenant_id`, `credential_id`, `connection_id`, plaintext
ou ciphertext como label, atributo ou payload.
