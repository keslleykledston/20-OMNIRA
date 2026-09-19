import React, { useEffect, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import axios from 'axios';
import { useNavigate } from 'react-router-dom';
import { API_BASE } from '../lib/config';
import { useRealtimeEvents } from '../hooks/useRealtimeEvents';
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from '../lib/session';
import { ConversationItem } from '../types/api';


/**
 * InboxPage — lista de conversas com paginação cursor-based
 * Consumes M05.1 API: GET /tenants/{tenant_id}/inbox/conversations
 */
export function InboxPage() {
  const tenantId = getTenantId();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [cursor, setCursor] = useState<string | null>(null);
  const [conversations, setConversations] = useState<ConversationItem[]>([]);
  const [hasMore, setHasMore] = useState(false);

  const { data, isLoading, error } = useQuery({
    queryKey: ['conversations', tenantId, cursor],
    queryFn: async () => {
      const params: any = { limit: 20 };
      if (cursor) params.cursor = cursor;
      try {
        const res = await axios.get(`${API_BASE}/tenants/${tenantId}/inbox/conversations`, { params, headers: authHeaders() });
        return res.data;
      } catch (err) {
        if (isUnauthorized(err)) handleUnauthorized();
        throw err;
      }
    },
    enabled: !!tenantId,
  });

  // New messages / assignment changes anywhere in the tenant: refetch the list.
  useRealtimeEvents({
    tenantId,
    onEvent: () => {
      void queryClient.invalidateQueries({ queryKey: ['conversations', tenantId] });
    },
    onReconnect: () => {
      void queryClient.invalidateQueries({ queryKey: ['conversations', tenantId] });
    },
  });

  useEffect(() => {
    if (data) {
      setConversations((prev) => mergeConversations(prev, data.items || []));
      setHasMore(data.has_more || false);
    }
  }, [data]);

  return (
    <div className="inbox-page">
      <header className="inbox-header">
        <h1>Inbox</h1>
        <p className="subtitle">{conversations.length} conversations</p>
      </header>

      {isLoading && <div className="loading">Loading conversations...</div>}
      {error && <div className="error">Failed to load conversations</div>}

      <div className="conversation-list">
        {conversations.map((conv) => (
          <div
            key={conv.id}
            className="conversation-item"
            role="link"
            tabIndex={0}
            onClick={() => navigate(`/inbox/${conv.id}`)}
            onKeyDown={(e) => e.key === 'Enter' && navigate(`/inbox/${conv.id}`)}
          >
            <div className="conversation-avatar">{conv.contact_name?.[0]?.toUpperCase()}</div>
            <div className="conversation-content">
              <h3 className="contact-name">{conv.contact_name}</h3>
              <p className="contact-phone">{conv.contact_phone}</p>
              <p className="conversation-status" data-status={conv.status}>
                {conv.status}
              </p>
            </div>
            <div className="conversation-meta">
              <p className="timestamp">{formatTime(conv.updated_at)}</p>
              {conv.assigned_to_user_id && (
                <p className="assigned-badge">Assigned</p>
              )}
            </div>
          </div>
        ))}
      </div>

      {conversations.length === 0 && !isLoading && (
        <div className="empty-state">
          <p>No conversations yet</p>
        </div>
      )}

      {hasMore && (
        <button className="load-more" onClick={() => setCursor(data.next_cursor)}>
          Load More
        </button>
      )}

      <style>{`
        .inbox-page {
          padding: 16px;
          max-width: 800px;
          margin: 0 auto;
        }
        .inbox-header {
          margin-bottom: 24px;
        }
        .inbox-header h1 {
          font-size: 28px;
          font-weight: 700;
          margin: 0;
        }
        .subtitle {
          color: #666;
          margin: 8px 0 0;
        }
        .conversation-list {
          display: flex;
          flex-direction: column;
          gap: 8px;
        }
        .conversation-item {
          display: flex;
          gap: 12px;
          padding: 12px;
          border-radius: 8px;
          background: #f5f5f5;
          cursor: pointer;
          transition: background 0.2s;
        }
        .conversation-item:hover {
          background: #eee;
        }
        .conversation-avatar {
          width: 48px;
          height: 48px;
          border-radius: 50%;
          background: #007AFF;
          color: white;
          display: flex;
          align-items: center;
          justify-content: center;
          font-weight: 600;
          flex-shrink: 0;
        }
        .conversation-content {
          flex: 1;
          min-width: 0;
        }
        .contact-name {
          margin: 0;
          font-size: 16px;
          font-weight: 600;
        }
        .contact-phone {
          margin: 4px 0;
          font-size: 14px;
          color: #666;
        }
        .conversation-status {
          margin: 4px 0 0;
          font-size: 12px;
          padding: 2px 8px;
          background: #e0e0e0;
          width: fit-content;
          border-radius: 4px;
        }
        .conversation-status[data-status="active"] {
          background: #d4edda;
          color: #155724;
        }
        .conversation-meta {
          text-align: right;
          flex-shrink: 0;
        }
        .timestamp {
          margin: 0;
          font-size: 12px;
          color: #999;
        }
        .assigned-badge {
          margin: 4px 0 0;
          font-size: 11px;
          background: #007AFF;
          color: white;
          padding: 2px 6px;
          border-radius: 3px;
          width: fit-content;
          margin-left: auto;
        }
        .empty-state {
          text-align: center;
          padding: 40px 20px;
          color: #999;
        }
        .load-more {
          width: 100%;
          padding: 12px;
          margin-top: 16px;
          background: #007AFF;
          color: white;
          border: none;
          border-radius: 8px;
          cursor: pointer;
          font-weight: 600;
        }
        .load-more:hover {
          background: #0051d5;
        }
      `}</style>
    </div>
  );
}

function mergeConversations(prev: ConversationItem[], fresh: ConversationItem[]): ConversationItem[] {
  const byId = new Map(prev.map((c) => [c.id, c] as const));
  fresh.forEach((c) => byId.set(c.id, c));
  return Array.from(byId.values()).sort((a, b) => ((a.created_at ?? '') < (b.created_at ?? '') ? 1 : -1));
}

function formatTime(isoString: string): string {
  const date = new Date(isoString);
  const now = new Date();
  const diff = now.getTime() - date.getTime();
  const minutes = Math.floor(diff / 60000);
  const hours = Math.floor(diff / 3600000);
  const days = Math.floor(diff / 86400000);

  if (minutes < 1) return 'now';
  if (minutes < 60) return `${minutes}m ago`;
  if (hours < 24) return `${hours}h ago`;
  if (days < 7) return `${days}d ago`;
  return date.toLocaleDateString();
}
