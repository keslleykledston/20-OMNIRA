-- provider_chat_id: endereço canônico da conversa no provedor.
--
-- O WhatsApp passou a endereçar contatos por "@lid" (Linked ID) em vez do
-- telefone. Reconstruir o destino como "<telefone>@c.us" a partir do contato
-- produz um endereço que o provedor ACEITA (ack=1 SERVER) e NUNCA ENTREGA:
-- observado em produção 2026-09-20, com a mesma mensagem chegando a ack=2
-- DEVICE quando enviada ao "@lid" correspondente.
--
-- Guardar o endereço tal como o provedor o informou é o que permite responder
-- no mesmo endereço em que a mensagem chegou. Vazio significa "sem endereço
-- conhecido", e o envio cai no comportamento antigo (derivar do telefone).
ALTER TABLE conversations
  ADD COLUMN IF NOT EXISTS provider_chat_id TEXT NOT NULL DEFAULT '';

COMMENT ON COLUMN conversations.provider_chat_id IS
  'Endereço da conversa no provedor, como ele o informou (ex.: 1752...@lid ou 5511...@c.us). Usado para responder no mesmo endereço; vazio = derivar do telefone.';
