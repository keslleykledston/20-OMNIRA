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
  message_count: number;
  unread_count: number;
  crm_contact_id?: string;
  participants?: ConversationParticipant[];
}

export interface MessageItem {
  id: string;
  conversation_id: string;
  body: string;
  direction: 'inbound' | 'outbound';
  status: 'queued' | 'sent' | 'delivered' | 'read' | 'failed' | 'pending' | 'uncertain';
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
