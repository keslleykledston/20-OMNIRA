// M05.1 - M05.2: Inbox API types (REST + SSE)

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
}

export interface MessageItem {
  id: string;
  conversation_id: string;
  body: string;
  direction: 'inbound' | 'outbound';
  status: 'queued' | 'sent' | 'delivered' | 'read' | 'failed' | 'pending';
  created_at: string;
  created_by?: string;
  media_urls?: string[];
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
