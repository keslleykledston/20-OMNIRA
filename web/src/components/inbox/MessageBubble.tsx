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

  // Time (and delivery mark) sit at the end of the text, WhatsApp style, so a one-line message is one line tall. With text
  // they are positioned in the bubble's bottom-right corner over an empty spacer that ends the last line; without text
  // they take their own line under the attachment.
  const timeMark = (extra: string) => (
    <span className={clsx('inline-flex items-center gap-0.5 text-[10px] leading-none text-text-tertiary', extra)}>
      <time>{new Date(message.created_at).toLocaleTimeString('pt-BR', {
        hour: '2-digit',
        minute: '2-digit'
      })}</time>
      {isOutbound && <span className={statusColor} title={statusTitle}>{deliverySymbol}</span>}
    </span>
  );

  return (
    <div className={clsx('flex', isOutbound ? 'flex-row-reverse' : 'flex-row')}>
      {/* Message Bubble */}
      <div className={clsx(
        'relative max-w-[85%] rounded-lg border px-2.5 py-1 text-[13.5px] leading-[1.35] sm:max-w-[75%]',
        isOutbound
          ? 'border-accent-primary/10 bg-accent-primary-soft text-text-primary'
          : 'border-border-subtle bg-surface text-text-primary'
      )}>
        {sender && <p className="mb-0.5 text-[10px] font-medium leading-tight text-text-tertiary">{sender}</p>}
        {message.body && (
          <>
            <p className="whitespace-pre-wrap break-words">
              {message.body}
              <span aria-hidden="true" className={clsx('inline-block', isOutbound ? 'w-14' : 'w-10')} />
            </p>
            {timeMark('absolute bottom-1 right-2')}
          </>
        )}

        {/* Media content */}
        {tenantId && <MessageMedia message={message} tenantId={tenantId} />}

        {/* Without text (media only) the time goes on its own line under the attachment */}
        {!message.body && <div className="flex justify-end">{timeMark('')}</div>}

        {isOutbound && message.status === 'failed' && (
          <p role="status" className="mt-0.5 text-[11px] font-medium leading-tight text-status-danger">
            Não foi entregue{reason ? `: ${reason}` : '.'}
          </p>
        )}
      </div>
    </div>
  );
}
