import React from 'react';
import clsx from 'clsx';
import { MessageItem } from '../../types/api';
import { Icon } from '../primitives';
import MessageMedia from './MessageMedia';
import { getTenantId } from '../../lib/session';

interface MessageBubbleProps {
  message: MessageItem;
}

export default function MessageBubble({ message }: MessageBubbleProps) {
  const tenantId = getTenantId();
  const isOutbound = message.direction === 'outbound';

  // Delivery status visual symbol
  const deliverySymbol =
    message.status === 'failed' ? '⚠' :
    message.status === 'uncertain' ? '?' :
    message.status === 'pending' || message.status === 'queued' ? '◷' :
    message.status === 'sent' ? '✓' :
    message.status === 'delivered' ? '✓✓' :
    message.status === 'read' ? '✓✓' :
    '';

  const statusColor =
    message.status === 'read' ? 'text-accent-primary' :
    message.status === 'failed' ? 'text-status-danger' :
    message.status === 'uncertain' ? 'text-text-secondary' :
    'text-text-tertiary';

  // PILOT.4A2: 'uncertain' means OMNIRA could not prove the provider outcome
  // — never say "failed" for it, and never suggest an automatic resend.
  const statusTitle =
    message.status === 'uncertain' ? 'A entrega não pôde ser confirmada' :
    undefined;

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
        {message.body && <p className="break-words">{message.body}</p>}

        {/* Media content */}
        {tenantId && <MessageMedia message={message} tenantId={tenantId} />}

        {/* Timestamp inside bubble */}
        <footer className={clsx(
          'mt-1 text-xs flex items-center justify-end gap-1',
          isOutbound ? 'text-white/75' : 'text-text-secondary'
        )}>
          <time>{new Date(message.created_at).toLocaleTimeString('pt-BR', {
            hour: '2-digit',
            minute: '2-digit'
          })}</time>
          {isOutbound && <span className={statusColor} title={statusTitle}>{deliverySymbol}</span>}
        </footer>
      </div>
    </div>
  );
}
