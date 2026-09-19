import React, { useEffect, useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import axios from 'axios';
import { MessageItem, ConversationItem, RealtimeEvent } from '../types/api';
import { useRealtimeEvents } from '../hooks/useRealtimeEvents';
import { AssignmentButton } from '../components/AssignmentButton';
import { authHeaders } from '../lib/session';

const API_BASE = 'http://localhost:8080/api/v1';

interface ConversationPageProps {
  conversationId: string;
}

/**
 * ConversationPage — detalhe de conversa com mensagens e SSE realtime
 * Consumes M05.1 API: GET /tenants/{tenant_id}/inbox/conversations/{conversation_id}/messages
 * Consumes M05.2 SSE: GET /tenants/{tenant_id}/inbox/conversations/{conversation_id}/events
 */
export function ConversationPage({ conversationId }: ConversationPageProps) {
  const tenantId = getTenantIdFromAuth();
  const [messages, setMessages] = useState<MessageItem[]>([]);
  const [conversation, setConversation] = useState<ConversationItem | null>(null);
  const [cursor, setCursor] = useState<string | null>(null);
  const [hasMore, setHasMore] = useState(false);
  const [messageText, setMessageText] = useState('');
  const [sending, setSending] = useState(false);
  const [sendError, setSendError] = useState<string | null>(null);
  // One Idempotency-Key per distinct text: a retry after a network failure or a
  // timeout re-sends the same key, so the backend never queues a duplicate.
  const pendingSend = useRef<{ text: string; key: string } | null>(null);

  // Load messages via REST API (M05.1)
  const { data: messagesData, isLoading: messagesLoading } = useQuery({
    queryKey: ['messages', tenantId, conversationId, cursor],
    queryFn: async () => {
      const params: any = { limit: 50 };
      if (cursor) params.cursor = cursor;
      const res = await axios.get(
        `${API_BASE}/tenants/${tenantId}/inbox/conversations/${conversationId}/messages`,
        { params, headers: authHeaders() }
      );
      return res.data;
    },
  });

  useEffect(() => {
    if (messagesData) {
      setMessages(messagesData.items || []);
      setHasMore(messagesData.has_more || false);
    }
  }, [messagesData]);

  // Subscribe to realtime events (M05.2)
  useRealtimeEvents({
    tenantId,
    conversationId,
    onEvent: (event: RealtimeEvent) => {
      if (event.type === 'message_received') {
        const newMessage = event.data as MessageItem;
        setMessages((prev) => [newMessage, ...prev]);
      } else if (event.type === 'status_changed') {
        setConversation((prev) => prev ? { ...prev, status: event.data.status } : null);
      }
    },
    onError: (error) => {
      console.error('Realtime error:', error);
    },
  });

  const handleSendMessage = async (e: React.FormEvent) => {
    e.preventDefault();
    const text = messageText.trim();
    if (!text || sending) return;
    if (!pendingSend.current || pendingSend.current.text !== text) {
      pendingSend.current = { text, key: newIdempotencyKey() };
    }
    setSending(true);
    setSendError(null);
    try {
      const res = await axios.post(
        `${API_BASE}/tenants/${tenantId}/inbox/conversations/${conversationId}/messages`,
        { text },
        { headers: { ...authHeaders(), 'Idempotency-Key': pendingSend.current.key } }
      );
      const sent = res.data as MessageItem;
      setMessages((prev) => (prev.some((m) => m.id === sent.id) ? prev : [sent, ...prev]));
      setMessageText('');
      pendingSend.current = null;
    } catch (err: any) {
      // Keep the text and the key: retrying is safe (idempotent).
      setSendError(sendErrorMessage(err));
    } finally {
      setSending(false);
    }
  };

  return (
    <div className="conversation-page">
      <header className="conversation-header">
        <button className="back-button">← Inbox</button>
        <div className="header-info">
          <h1>{conversation?.contact_name || 'Loading...'}</h1>
          <p className="phone">{conversation?.contact_phone}</p>
        </div>
        <div className="header-controls">
          <div className="header-status" data-status={conversation?.status || 'active'}>
            {conversation?.status}
          </div>
          <AssignmentButton
            conversationId={conversationId}
            assignedToUserId={conversation?.assigned_to_user_id}
            onAssignmentChange={(userId) => {
              if (conversation) {
                setConversation({ ...conversation, assigned_to_user_id: userId });
              }
            }}
          />
        </div>
      </header>

      <div className="messages-container">
        {messagesLoading && <div className="loading">Loading messages...</div>}

        <div className="messages-list">
          {messages.map((msg) => (
            <div key={msg.id} className={`message message-${msg.direction}`}>
              <div className="message-body">{msg.body}</div>
              <div className="message-meta">
                <span className="timestamp">{formatTime(msg.created_at)}</span>
                <span className="status" data-status={msg.status}>
                  {msg.status}
                </span>
              </div>
              {msg.media_urls && msg.media_urls.length > 0 && (
                <div className="media">
                  {msg.media_urls.map((url, i) => (
                    <img key={i} src={url} alt="Media" className="media-thumb" />
                  ))}
                </div>
              )}
            </div>
          ))}
        </div>

        {hasMore && (
          <button className="load-older" onClick={() => setCursor(messagesData.next_cursor)}>
            Load Older Messages
          </button>
        )}
      </div>

      <form className="message-input-form" onSubmit={handleSendMessage}>
        <input
          type="text"
          value={messageText}
          onChange={(e) => setMessageText(e.target.value)}
          placeholder="Type a message..."
          className="message-input"
        />
        <button type="submit" className="send-button" disabled={sending}>
          {sending ? 'Sending...' : 'Send'}
        </button>
      </form>
      {sendError && (
        <div className="send-error" role="alert">
          {sendError}
        </div>
      )}

      <style>{`
        .send-error {
          color: #ff3b30;
          font-size: 12px;
          padding: 4px 16px 8px;
        }
        .conversation-page {
          display: flex;
          flex-direction: column;
          height: 100vh;
          background: white;
        }
        .conversation-header {
          display: flex;
          gap: 12px;
          padding: 12px 16px;
          border-bottom: 1px solid #e0e0e0;
          align-items: center;
          justify-content: space-between;
        }
        .back-button {
          padding: 8px 12px;
          border: none;
          background: none;
          cursor: pointer;
          font-weight: 500;
          color: #007AFF;
          flex-shrink: 0;
        }
        .header-info {
          flex: 1;
          min-width: 0;
        }
        .header-info h1 {
          margin: 0;
          font-size: 18px;
          font-weight: 600;
        }
        .phone {
          margin: 4px 0 0;
          font-size: 12px;
          color: #999;
        }
        .header-controls {
          display: flex;
          gap: 8px;
          align-items: center;
          flex-shrink: 0;
        }
        .header-status {
          padding: 4px 8px;
          font-size: 11px;
          background: #e0e0e0;
          border-radius: 4px;
          font-weight: 500;
        }
        .header-status[data-status="active"] {
          background: #d4edda;
          color: #155724;
        }
        .messages-container {
          flex: 1;
          overflow-y: auto;
          padding: 16px;
          display: flex;
          flex-direction: column-reverse;
        }
        .messages-list {
          display: flex;
          flex-direction: column-reverse;
          gap: 12px;
        }
        .message {
          max-width: 80%;
          padding: 10px 12px;
          border-radius: 8px;
          background: #f5f5f5;
        }
        .message-outbound {
          align-self: flex-end;
          background: #007AFF;
          color: white;
        }
        .message-body {
          word-wrap: break-word;
          font-size: 14px;
          line-height: 1.4;
        }
        .message-meta {
          display: flex;
          gap: 8px;
          margin-top: 4px;
          font-size: 11px;
          opacity: 0.7;
        }
        .status {
          padding: 2px 4px;
        }
        .media {
          display: flex;
          gap: 4px;
          margin-top: 8px;
        }
        .media-thumb {
          max-width: 120px;
          border-radius: 4px;
        }
        .load-older {
          align-self: center;
          padding: 8px 16px;
          margin: 16px 0;
          background: #f5f5f5;
          border: 1px solid #e0e0e0;
          border-radius: 4px;
          cursor: pointer;
        }
        .message-input-form {
          display: flex;
          gap: 8px;
          padding: 16px;
          border-top: 1px solid #e0e0e0;
          background: white;
        }
        .message-input {
          flex: 1;
          padding: 10px 12px;
          border: 1px solid #e0e0e0;
          border-radius: 6px;
          font-size: 14px;
        }
        .send-button {
          padding: 10px 20px;
          background: #007AFF;
          color: white;
          border: none;
          border-radius: 6px;
          cursor: pointer;
          font-weight: 600;
        }
        .send-button:hover {
          background: #0051d5;
        }
        .loading {
          padding: 20px;
          text-align: center;
          color: #999;
        }
      `}</style>
    </div>
  );
}

function newIdempotencyKey(): string {
  return typeof crypto !== 'undefined' && 'randomUUID' in crypto
    ? crypto.randomUUID()
    : `k-${Date.now()}-${Math.random().toString(36).slice(2, 12)}`;
}

// Backend answers plain-text errors: 409 not assigned to you yet / no active
// channel, 403 assigned to another agent, 404 not visible in this tenant, 422 bad text.
export function sendErrorMessage(err: any): string {
  const status = err?.response?.status;
  const detail = typeof err?.response?.data === 'string' ? err.response.data : '';
  switch (status) {
    case 409:
      return detail.includes('assigned')
        ? 'Assign this conversation to yourself before replying'
        : 'This conversation has no active WhatsApp channel';
    case 403:
      return 'This conversation is assigned to another agent';
    case 404:
      return 'Conversation not found';
    case 422:
      return 'Message is empty or too long';
    default:
      return 'Failed to send message';
  }
}

function getTenantIdFromAuth(): string {
  return localStorage.getItem('tenantId') || '00000000-0000-0000-0000-000000000000';
}

function formatTime(isoString: string): string {
  const date = new Date(isoString);
  return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}
