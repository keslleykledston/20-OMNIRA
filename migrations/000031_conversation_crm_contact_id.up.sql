-- R5: Adiciona vínculo entre conversation e contato no CRM K3G.
-- 
-- Quando inbound chega do WhatsApp, o webhook invoca FindCustomerByPhone no
-- CRM. Se não existe, cria novo contato automaticamente. O UUID retornado fica
-- gravado aqui para reuso: próximo atendimento do mesmo número reutiliza o
-- mesmo contactId sem duplicata.
--
-- Não há foreign key no CRM (apenas tracking), porque o CRM é sistema externo.
-- RLS: coluna é permissão implícita (é parte da conversation, que já respeita RLS).
ALTER TABLE conversations ADD COLUMN crm_contact_id UUID;
