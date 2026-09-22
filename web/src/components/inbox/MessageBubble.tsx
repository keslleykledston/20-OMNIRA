import React from 'react';
import clsx from 'clsx';
import { MessageItem } from '../../types/api';
import { Icon } from '../primitives';

interface MessageBubbleProps {
  message: MessageItem;
}

export default function MessageBubble({ message }: MessageBubbleProps) {
  const isOutbound = message.direction === 'outbound';

  // Delivery status visual symbol
  const deliverySymbol =
    message.status === 'failed' ? '⚠' :
    message.status === 'pending' || message.status === 'queued' ? '◷' :
    message.status === 'sent' ? '✓' :
    message.status === 'delivered' ? '✓✓' :
    message.status === 'read' ? '✓✓' :
    '';

  const statusColor =
    message.status === 'read' ? 'text-accent-primary' :
    message.status === 'failed' ? 'text-status-danger' :
    'text-text-tertiary';

  return (
    <div className={clsx(
      'flex gap-2',
      isOutbound ? 'flex-row-reverse' : 'flex-row'
    )}>
      {/* Message Bubble */}
      <div className={clsx(
        'max-w-xs rounded-lg p-3 text-sm leading-relaxed',
        isOutbound
          ? 'bg-accent-primary text-white rounded-br-none'
          : 'bg-surface-muted text-text-primary rounded-bl-none'
      )}>
        <p className="break-words">{message.body}</p>

        {/* Timestamp inside bubble */}
        <footer className={clsx(
          'mt-1 text-xs flex items-center justify-end gap-1',
          isOutbound ? 'text-white/75' : 'text-text-secondary'
        )}>
          <time>{new Date(message.created_at).toLocaleTimeString('pt-BR', {
            hour: '2-digit',
            minute: '2-digit'
          })}</time>
          {isOutbound && <span className={statusColor}>{deliverySymbol}</span>}
        </footer>
      </div>
    </div>
  );
}
