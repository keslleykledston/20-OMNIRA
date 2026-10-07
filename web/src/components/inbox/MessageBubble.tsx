import React from 'react';
import clsx from 'clsx';
import { MessageItem } from '../../types/api';
import { Icon } from '../primitives';
import MessageMedia from './MessageMedia';
import { getTenantId } from '../../lib/session';
import { explainFailure } from '../../lib/providerErrors';

interface MessageBubbleProps {
  message: MessageItem;
  /** Who is speaking, shown small above the text (the contact's name, or "Equipe" for our side). */
  sender?: string;
}

export default function MessageBubble({ message, sender }: MessageBubbleProps) {
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
  const explained = explainFailure(message.failure_reason);
  const reason = explained ? `${explained.text}${explained.code ? ` (código ${explained.code})` : ''}` : '';
  const statusTitle =
    message.status === 'uncertain' ? 'A entrega não pôde ser confirmada' :
    message.status === 'failed' && reason ? `Falha: ${reason}` :
    undefined;

  return (
    <div className={clsx(
      'flex gap-2',
      isOutbound ? 'flex-row-reverse' : 'flex-row'
    )}>
      {/* Message Bubble */}
      <div className={clsx(
        'max-w-[85%] rounded-lg border px-3 py-2 text-sm leading-snug sm:max-w-[75%]',
        isOutbound
          ? 'border-accent-primary/10 bg-accent-primary-soft text-text-primary'
          : 'border-border-subtle bg-surface text-text-primary'
      )}>
        {sender && <p className="mb-1 text-[10px] font-medium text-text-tertiary">{sender}</p>}
        {message.body && <p className="break-words">{message.body}</p>}

        {/* Media content */}
        {tenantId && <MessageMedia message={message} tenantId={tenantId} />}

        {/* Timestamp inside bubble */}
        <footer className={clsx(
          'mt-0.5 text-[11px] flex items-center justify-end gap-1',
          'text-text-tertiary'
        )}>
          <time>{new Date(message.created_at).toLocaleTimeString('pt-BR', {
            hour: '2-digit',
            minute: '2-digit'
          })}</time>
          {isOutbound && <span className={statusColor} title={statusTitle}>{deliverySymbol}</span>}
        </footer>
        {isOutbound && message.status === 'failed' && (
          <p role="status" className="mt-1 text-[11px] font-medium text-status-danger">
            Não foi entregue{reason ? `: ${reason}` : '.'}
          </p>
        )}
      </div>
    </div>
  );
}
