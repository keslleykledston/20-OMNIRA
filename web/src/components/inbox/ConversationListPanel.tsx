import React from 'react';
import clsx from 'clsx';
import { ConversationItem } from '../../types/api';
import { Icon } from '../primitives';

interface ConversationListPanelProps {
  conversations: ConversationItem[];
  selectedId: string | null;
  onSelect: (id: string) => void;
  segment: 'all' | 'unread' | 'mine';
  onSegmentChange: (segment: 'all' | 'unread' | 'mine') => void;
  isLoading: boolean;
}

export default function ConversationListPanel({
  conversations,
  selectedId,
  onSelect,
  segment,
  onSegmentChange,
  isLoading,
}: ConversationListPanelProps) {
  return (
    <div className="flex flex-col h-full bg-surface">
      {/* Segmented Control */}
      <div className="p-4 border-b border-border-subtle">
        <div className="flex gap-2">
          {(['all', 'unread', 'mine'] as const).map((s) => (
            <button
              key={s}
              onClick={() => onSegmentChange(s)}
              className={clsx(
                'px-3 py-1.5 text-sm font-medium rounded-control transition-colors',
                segment === s
                  ? 'bg-accent-primary text-white'
                  : 'bg-surface-muted text-text-secondary hover:bg-surface-tertiary'
              )}
            >
              {s === 'all' ? 'Todas' : s === 'unread' ? 'Não lidas' : 'Minhas'}
            </button>
          ))}
        </div>
      </div>

      {/* Search */}
      <div className="p-3 border-b border-border-subtle">
        <input
          type="text"
          placeholder="Buscar contato..."
          className={clsx(
            'w-full px-3 py-2 text-sm rounded-control',
            'bg-surface-muted text-text-primary',
            'placeholder:text-text-tertiary',
            'focus:outline-none focus:ring-2 focus:ring-accent-primary',
            'border border-transparent'
          )}
        />
      </div>

      {/* Conversation List */}
      <div className="flex-1 overflow-y-auto">
        {isLoading ? (
          <div className="p-4 text-center text-text-secondary text-sm">Carregando...</div>
        ) : conversations.length === 0 ? (
          <div className="p-4 text-center text-text-tertiary text-sm">Nenhuma conversa</div>
        ) : (
          conversations.map((conv) => (
            <div
              key={conv.id}
              onClick={() => onSelect(conv.id)}
              className={clsx(
                'p-3 border-b border-border-subtle cursor-pointer transition-colors',
                selectedId === conv.id
                  ? 'bg-accent-primary-soft'
                  : 'hover:bg-surface-muted'
              )}
            >
              {/* Row: Avatar + Content + Meta */}
              <div className="flex gap-3 items-start">
                {/* Avatar */}
                <div
                  className={clsx(
                    'w-10 h-10 rounded-full flex items-center justify-center',
                    'flex-shrink-0 font-semibold text-sm text-white',
                    'bg-accent-primary'
                  )}
                >
                  {conv.contact_name?.[0]?.toUpperCase() || '?'}
                </div>

                {/* Content */}
                <div className="flex-1 min-w-0">
                  <div className="flex items-baseline gap-2">
                    <h4 className="text-sm font-semibold text-text-primary truncate">
                      {conv.contact_name}
                    </h4>
                    {conv.unread_count > 0 && (
                      <span className="text-xs font-semibold bg-accent-primary text-white px-1.5 py-0.5 rounded-full flex-shrink-0">
                        {conv.unread_count}
                      </span>
                    )}
                  </div>
                  <p className="text-xs text-text-secondary truncate">
                    {conv.contact_phone}
                  </p>
                  <div className="flex gap-2 items-center mt-1">
                    <span className={clsx(
                      'text-xs px-1.5 py-0.5 rounded-pill font-medium',
                      conv.status === 'active' ? 'bg-status-success-soft text-status-success' :
                      conv.status === 'closed' ? 'bg-status-muted text-text-secondary' :
                      'bg-status-warning-soft text-status-warning'
                    )}>
                      {conv.status === 'active' ? 'Ativo' : conv.status === 'closed' ? 'Fechado' : 'Pendente'}
                    </span>
                    {conv.assigned_to_user_id && (
                      <span className="text-xs text-text-tertiary">Atribuído</span>
                    )}
                  </div>
                </div>

                {/* Time */}
                <div className="text-xs text-text-tertiary flex-shrink-0 text-right">
                  {formatTime(conv.updated_at)}
                </div>
              </div>
            </div>
          ))
        )}
      </div>
    </div>
  );
}

function formatTime(isoString: string): string {
  const date = new Date(isoString);
  const now = new Date();
  const diff = now.getTime() - date.getTime();
  const minutes = Math.floor(diff / 60000);
  const hours = Math.floor(diff / 3600000);
  const days = Math.floor(diff / 86400000);

  if (minutes < 1) return 'agora';
  if (minutes < 60) return `${minutes}m`;
  if (hours < 24) return `${hours}h`;
  if (days < 7) return `${days}d`;
  return date.toLocaleDateString('pt-BR', { month: 'short', day: 'numeric' });
}
