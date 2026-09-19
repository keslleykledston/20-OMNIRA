-- E2E fixtures (idempotent). Used by scripts/e2e-inbox.sh and scripts/cleanroom-compose.sh.
-- Tenant 1111.. = main tenant (agent test@ / admin admin@ of the mock login), tenant 2222.. = foreign tenant.
INSERT INTO users(id,external_subject,email,status) VALUES
 ('22222222-2222-2222-2222-222222222222','test@omnira.local','test@omnira.local','active'),
 ('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa','admin@omnira.local','admin@omnira.local','active')
 ON CONFLICT DO NOTHING;
INSERT INTO tenants(id,legal_name,status) VALUES
 ('11111111-1111-1111-1111-111111111111','E2E Co','active'),
 ('22222222-aaaa-aaaa-aaaa-aaaaaaaaaaaa','Other Tenant','active') ON CONFLICT DO NOTHING;
INSERT INTO memberships(tenant_id,user_id,role_id,status)
 SELECT '11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222',id,'active' FROM roles WHERE key='tenant_agent' AND tenant_id IS NULL
 ON CONFLICT DO NOTHING;
INSERT INTO memberships(tenant_id,user_id,role_id,status)
 SELECT '11111111-1111-1111-1111-111111111111','aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',id,'active' FROM roles WHERE key='tenant_admin' AND tenant_id IS NULL
 ON CONFLICT DO NOTHING;
INSERT INTO queues(tenant_id,name,mode,is_default) VALUES ('11111111-1111-1111-1111-111111111111','Default','manual',true) ON CONFLICT DO NOTHING;
INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities)
 VALUES ('c0000000-0000-0000-0000-00000000c001','11111111-1111-1111-1111-111111111111','whatsapp','waha','unofficial','e2e-number','active','["text"]')
 ON CONFLICT DO NOTHING;
INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES
 ('c0c0c0c0-0000-0000-0000-000000000001','11111111-1111-1111-1111-111111111111','Maria Souza','+5511988887777'),
 ('c0c0c0c0-0000-0000-0000-0000000000ff','22222222-aaaa-aaaa-aaaa-aaaaaaaaaaaa','Bruno Outro','+5511966665555')
 ON CONFLICT DO NOTHING;
INSERT INTO conversations(id,tenant_id,contact_id,channel_connection_id,status) VALUES
 ('d0d0d0d0-0000-0000-0000-000000000001','11111111-1111-1111-1111-111111111111','c0c0c0c0-0000-0000-0000-000000000001','c0000000-0000-0000-0000-00000000c001','open')
 ON CONFLICT DO NOTHING;
INSERT INTO conversations(id,tenant_id,contact_id,status) VALUES
 ('d0d0d0d0-0000-0000-0000-0000000000ff','22222222-aaaa-aaaa-aaaa-aaaaaaaaaaaa','c0c0c0c0-0000-0000-0000-0000000000ff','open')
 ON CONFLICT DO NOTHING;
INSERT INTO messages(tenant_id,conversation_id,channel_connection_id,direction,message_type,body,provider_message_id,status)
 VALUES ('11111111-1111-1111-1111-111111111111','d0d0d0d0-0000-0000-0000-000000000001','c0000000-0000-0000-0000-00000000c001','inbound','text','Olá, preciso de ajuda com meu pedido','e2e-seed-1','received')
 ON CONFLICT DO NOTHING;
INSERT INTO messages(tenant_id,conversation_id,direction,message_type,body,provider_message_id,status)
 VALUES ('22222222-aaaa-aaaa-aaaa-aaaaaaaaaaaa','d0d0d0d0-0000-0000-0000-0000000000ff','inbound','text','mensagem secreta do outro tenant','e2e-seed-x','received')
 ON CONFLICT DO NOTHING;
