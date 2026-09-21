import React from 'react';
import clsx from 'clsx';
import { MessageItem } from '../../types/api';
import { Icon } from '../primitives';

interface MessageBubbleProps {
  message: MessageItem;
}

export default function MessageBubble({ message }: MessageBubbleProps) {
  const isOutbound = message.direction === 'outbound';
  const statusIcon =
    message.status === 'read' ? 'check' :
    message.status === 'delivered' ? 'check' :
    message.status === 'sent' ? 'check' :
    message.status === 'failed' ? 'info' :
    'clock';

  return (
    <div className={clsx(
      'flex gap-3',
      isOutbound ? 'flex-row-reverse' : 'flex-row'
    )}>
      {/* Message Bubble */}
      <div className={clsx(
        'max-w-xs rounded-control p-3 text-sm',
        isOutbound
          ? 'bg-accent-primary text-white rounded-br-none'
          : 'bg-surface-muted text-text-primary rounded-bl-none'
      )}>
        <p className="leading-relaxed break-words">{message.body}</p>
      </div>

      {/* Status (outbound only) */}
      {isOutbound && (
        <div className="flex items-end gap-1">
          <div className="text-xs text-text-tertiary">
            {new Date(message.created_at).toLocaleTimeString('pt-BR', {
              hour: '2-digit',
              minute: '2-digit'
            })}
          </div>
          <Icon
            name={statusIcon}
            className={clsx(
              'w-4 h-4',
              message.status === 'failed' && 'text-status-danger',
              message.status === 'read' && 'text-accent-primary'
            )}
          />
        </div>
      )}

      {/* Time (inbound only) */}
      {!isOutbound && (
        <div className="text-xs text-text-tertiary">
          {new Date(message.created_at).toLocaleTimeString('pt-BR', {
            hour: '2-digit',
            minute: '2-digit'
          })}
        </div>
      )}
    </div>
  );
}
