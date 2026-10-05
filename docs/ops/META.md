# WhatsApp oficial (Meta Cloud API)

Estágio 1: texto, mídia recebida, status de entrega. Templates (fora da janela de 24 h) = estágio 2.

## Modelo
- Uma conexão por número, no mesmo tenant da WAHA. Credenciais **por conexão**, cifradas (ADR-0009):
  `access_token`, `app_secret`, `verify_token` (gerado pelo OMNIRA). Não existe segredo global (`OMNIRA_META_*` removidos).
- Webhook único público: `POST/GET /webhooks/v1/whatsapp/meta`.
  - POST: acha a conexão por `metadata.phone_number_id`, valida `X-Hub-Signature-256` com o `app_secret` **daquela** conexão.
    Número desconhecido, corpo inválido e assinatura errada respondem igual (401).
  - GET (handshake): `verify_token` = `omn-<connection_id>-<aleatório>`; o id localiza a conexão, comparação em tempo constante.
- Envio: `POST /{phone_number_id}/messages`. A Meta **não tem chave de idempotência**: timeout/5xx depois de enviar
  vira `uncertain` (terminal, nunca reenvia sozinho). Só 429/throttle e falhas de conexão são retentadas.
- Janela de 24 h fechada (erro 131047) falha como `window_closed`.
- Mídia recebida: id → URL (host em allowlist Meta) → download com o token da conexão → pipeline ADR-0016 (ClamAV).

## Ligar
1. `OMNIRA_META_ENABLED=true` no `.env` (api e worker). `OMNIRA_PUBLIC_BASE_URL` = domínio público.
2. Canais → Adicionar canal → WhatsApp oficial: Phone Number ID, WABA ID, token permanente, App Secret.
3. Copiar Callback URL + Verify Token para Meta → WhatsApp → Configuration → Webhook; assinar `messages`.
4. "Testar conexão" (leitura apenas): a conexão só fica `active` se a Meta respondeu.
