// Dados 100% fictícios para as capturas de tela da documentação. Nenhum nome, telefone ou empresa é real.
export const TENANT = '11111111-1111-4111-8111-111111111111'
export const ME = { id: 'u-marina', email: 'marina@empresa-exemplo.test', name: 'Marina Costa' }

// Datas relativas a "agora" (as telas mostram "há 3 min", "aguardando 20 min"): a conversa de exemplo começou há ~1 h.
const BASE_MS = Math.floor((Date.now() - 100 * 60_000) / 60_000) * 60_000
const iso = (min) => new Date(BASE_MS + min * 60_000).toISOString()
export const T = iso

export const QUEUES = [
  { id: 'q-comercial', name: 'Comercial', mode: 'round_robin', is_default: false, member_count: 3, available_count: 2, open_conversation_count: 1 },
  { id: 'q-suporte', name: 'Suporte', mode: 'round_robin', is_default: true, member_count: 5, available_count: 4, open_conversation_count: 2 },
  { id: 'q-financeiro', name: 'Financeiro', mode: 'round_robin', is_default: false, member_count: 2, available_count: 1, open_conversation_count: 0 },
].map((q) => ({ created_at: iso(-90000), updated_at: iso(-90000), ...q }))

export const SECTORS = [
  { id: 'sec-1', name: 'Atendimento ao cliente', active: true, calendar_id: 'cal-1', description: 'Comercial, suporte e financeiro', color: 'blue', supervisor: { user_id: 'u-marina', name: 'Marina Costa' }, queues: [{ id: 'q-comercial', name: 'Comercial' }, { id: 'q-suporte', name: 'Suporte' }, { id: 'q-financeiro', name: 'Financeiro' }] },
]
export const CALENDARS = [
  { id: 'cal-1', name: 'Horário comercial', kind: 'business', timezone: 'America/Manaus', revision: 3, in_use: 1, updated_at: iso(-50000) },
  { id: 'cal-2', name: 'Plantão 24 horas', kind: '24x7', timezone: 'America/Manaus', revision: 1, in_use: 0, updated_at: iso(-60000) },
]

export const CONTACTS = [
  { id: 'c-ana', display_name: 'Ana Souza', phone_e164: '+5511900000001', email: 'ana@cliente-a.test', status: 'active', kind: 'customer', channels: ['whatsapp'], open_conversation_count: 1, last_interaction_at: iso(40) },
  { id: 'c-bruno', display_name: 'Bruno Lima', phone_e164: '+5511900000002', email: '', status: 'active', kind: 'customer', channels: ['whatsapp'], open_conversation_count: 1, last_interaction_at: iso(20) },
  { id: 'c-carla', display_name: 'Carla Mendes', phone_e164: '+5511900000003', email: 'carla@cliente-c.test', status: 'active', kind: 'other', channels: ['whatsapp'], open_conversation_count: 0, last_interaction_at: iso(-300) },
  { id: 'c-daniel', display_name: 'Daniel Rocha', phone_e164: '+5511900000004', email: '', status: 'active', kind: 'unclassified', channels: ['whatsapp'], open_conversation_count: 1, last_interaction_at: iso(5) },
].map((c) => ({ created_at: iso(-5000), updated_at: iso(0), subject_type: 'contact', account_count: 0, ...c }))

export const CONVERSATIONS = [
  { id: 'v-ana', contact_id: 'c-ana', contact_name: 'Ana Souza', contact_phone: '+5511900000001', status: 'open', queue_id: 'q-suporte', assigned_to_user_id: 'u-marina', assigned_to_name: 'Marina Costa', last_message_at: iso(40), last_message_direction: 'inbound', last_message_type: 'text', last_message_preview: 'Perfeito, muito obrigada!', waiting_since: iso(40), contact_kind: 'customer', conversation_kind: 'customer_service', channel_connection_id: 'ch-1' },
  { id: 'v-bruno', contact_id: 'c-bruno', contact_name: 'Bruno Lima', contact_phone: '+5511900000002', status: 'open', queue_id: 'q-comercial', last_message_at: iso(20), last_message_direction: 'inbound', last_message_type: 'text', last_message_preview: 'Quero um orçamento para 10 licenças', waiting_since: iso(20), contact_kind: 'customer', conversation_kind: 'customer_service', channel_connection_id: 'ch-1' },
  { id: 'v-daniel', contact_id: 'c-daniel', contact_name: 'Daniel Rocha', contact_phone: '+5511900000004', status: 'open', last_message_at: iso(5), last_message_direction: 'inbound', last_message_type: 'text', last_message_preview: 'Oi, tudo bem?', waiting_since: iso(5), contact_kind: 'unclassified', conversation_kind: 'unclassified', channel_connection_id: 'ch-1' },
].map((c) => ({ created_at: iso(-60), updated_at: c.last_message_at, ...c }))

export const MESSAGES_ANA = [
  { id: 'm1', direction: 'inbound', message_type: 'text', body: 'Olá, preciso de ajuda com a minha fatura de outubro.', status: 'received', created_at: iso(0) },
  { id: 'm2', direction: 'outbound', message_type: 'text', body: 'Olá! Sou o assistente virtual. Em que posso ajudar? Digite 1 para Financeiro, 2 para Suporte.', status: 'delivered', sender_kind: 'automation', created_at: iso(1) },
  { id: 'm3', direction: 'inbound', message_type: 'text', body: '2', status: 'received', created_at: iso(2) },
  { id: 'm4', direction: 'outbound', message_type: 'text', body: 'Oi, Ana! Aqui é a Marina, do suporte. Já estou vendo a sua fatura.', status: 'read', sender_kind: 'agent', created_at: iso(10) },
  { id: 'm5', direction: 'outbound', message_type: 'text', body: 'Corrigi o valor e reenviei o boleto para o seu e-mail.', status: 'read', sender_kind: 'agent', created_at: iso(25) },
  { id: 'm6', direction: 'inbound', message_type: 'text', body: 'Perfeito, muito obrigada!', status: 'received', created_at: iso(40) },
]

export const TIMELINE_ANA = [
  { id: 'assignment:1', kind: 'auto_assigned', at: iso(3), target: 'Marina Costa', target_id: 'u-marina', system: true },
  { id: 'protocol_opened:1', kind: 'protocol_opened', at: iso(3.5), protocol: 'EXE-1042' },
]

export const TICKETS = [
  { id: 't1', conversation_id: 'v-ana', subject: 'Boleto de outubro com valor errado', status: 'in_progress', priority: 'high', created_at: iso(-30), updated_at: iso(25) },
  { id: 't2', conversation_id: 'v-bruno', subject: 'Orçamento de 10 licenças', status: 'open', priority: 'medium', created_at: iso(-20), updated_at: iso(20) },
  { id: 't3', conversation_id: 'v-x', subject: 'Instabilidade no acesso ao painel', status: 'open', priority: 'critical', created_at: iso(-300), updated_at: iso(-120) },
  { id: 't4', conversation_id: 'v-y', subject: 'Dúvida sobre nota fiscal', status: 'waiting', priority: 'low', created_at: iso(-700), updated_at: iso(-600) },
].map((t) => ({ assigned_to: null, provider: null, external_ticket_id: null, external_status: null, external_status_label: null, sync_status: null, last_synced_at: null, ...t }))

export const SESSION_ANA = { id: 's-ana', protocol: 'EXE-1042', external_protocol: null, conversation_id: 'v-ana', contact_id: 'c-ana', customer_id: null, queue_id: 'q-suporte', category_id: null, status: 'in_progress', resolution_code: null, resolution_note: '', origin_channel: 'whatsapp', opened_at: iso(3.5), closed_at: null, reopen_count: 0, version: 1 }

export const RESULT_CODES = [
  { id: 'r1', code: 'resolved', canonical_code: 'resolved_first_contact', label: 'Resolvido no primeiro contato', active: true },
  { id: 'r2', code: 'info', canonical_code: 'information_provided', label: 'Informação prestada', active: true },
  { id: 'r3', code: 'other', canonical_code: 'other', label: 'Outro', active: true },
]

export const APPS = [
  { id: 'app-1', name: 'CRM da Empresa Exemplo', status: 'active', scopes: ['conversation.read', 'message.read', 'contact.read', 'media.read', 'events.read', 'conversation.claim', 'message.send', 'conversation.transfer', 'conversation.finalize'], queue_ids: [], channel_ids: [], rate_limit_per_min: 600, lease_seconds: 900, allow_outside_hours: false, created_at: iso(-4000), updated_at: iso(-3000), suspended_at: null, revoked_at: null,
    credentials: [{ id: 'cr-1', key_id: 'a1b2c3d4e5', label: 'Produção', created_at: iso(-4000), expires_at: iso(60 * 24 * 90), revoked_at: null, last_used_at: iso(-2), live: true }] },
  { id: 'app-2', name: 'Painel de testes', status: 'suspended', scopes: ['conversation.read', 'message.read', 'events.read'], queue_ids: [], channel_ids: [], rate_limit_per_min: 300, lease_seconds: null, allow_outside_hours: false, created_at: iso(-9000), updated_at: iso(-8000), suspended_at: iso(-8000), revoked_at: null, credentials: [] },
]
