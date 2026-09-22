-- IAM2C: registrar a entrega do convite e a evidência de e-mail verificado do IdP.

-- NULL = o e-mail ainda não foi entregue (falha do provedor ou nenhum sender).
-- Preenchido a cada envio bem-sucedido, inclusive reenvios ("Enviado em").
ALTER TABLE membership_invitations ADD COLUMN sent_at TIMESTAMPTZ;

-- Vem do claim email_verified do ID token no login (nunca inferido). Default
-- false: sem evidência explícita do IdP, o e-mail não conta como confirmado.
ALTER TABLE user_identities ADD COLUMN email_verified BOOLEAN NOT NULL DEFAULT false;
