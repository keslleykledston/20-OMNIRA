// M05.1 - M05.2: Inbox API types (REST + SSE)

export interface ConversationParticipant {
  id: string;
  user_id: string;
  role: 'ASSIGNEE' | 'INVITED' | 'CO_ATTENDEE';
  joined_at?: string;
  left_at?: string;
}

export interface ConversationItem {
  id: string;
  contact_name: string;
  contact_phone: string;
  status: 'active' | 'closed' | 'pending';
  created_at?: string;
  updated_at: string;
  assigned_to_user_id?: string;
  // Single-conversation read only (absent in list items).
  message_count?: number;
  // List only: last real message, for ordering/preview, and when the customer started waiting.
  last_message_at?: string;
  last_message_direction?: 'inbound' | 'outbound';
  last_message_type?: string;
  last_message_preview?: string;
  waiting_since?: string;
  queue_id?: string;
  crm_contact_id?: string;
  contact_id?: string;
  // Classification of the conversation's contact (ADR-0014).
  contact_kind?: 'customer' | 'other' | 'spam';
  participants?: ConversationParticipant[];
}

export interface MessageItem {
  id: string;
  conversation_id: string;
  body: string;
  direction: 'inbound' | 'outbound';
  status: 'received' | 'queued' | 'sent' | 'delivered' | 'read' | 'failed' | 'pending' | 'uncertain';
  created_at: string;
  created_by?: string;
  message_type?: string; // 'text', 'image', 'document', etc.
  mime_type?: string; // 'image/jpeg', 'application/pdf', etc.
  size_bytes?: number; // Media file size in bytes
  media_urls?: string[]; // DEPRECATED: use /api/v1/tenants/{id}/messages/{id}/media instead
}

export interface PageResult<T> {
  items: T[];
  has_more: boolean;
  next_cursor?: string;
}

export interface ConversationPageResult extends PageResult<ConversationItem> {}
export interface MessagePageResult extends PageResult<MessageItem> {}

// M05.2 - SSE realtime events
export interface RealtimeEvent {
  type: string; // "conversation_created", "message_received", "status_changed"
  id: string; // conversation_id ou message_id
  timestamp: string;
  data: ConversationItem | MessageItem | any;
}
