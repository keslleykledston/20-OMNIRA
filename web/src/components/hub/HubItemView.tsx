import { useEffect, useRef } from 'react';
import clsx from 'clsx';
import type { HubItemDetail, HubMessage } from '../../lib/hub';
import { TenantBadge } from '../primitives/TenantBadge';

const TYPE_LABEL: Record<string, string> = { image: 'Imagem', video: 'Vídeo', audio: 'Áudio', document: 'Documento', sticker: 'Figurinha' };

// The Hub shows text only. Media is NOT rendered here on purpose: the tenant inbox fetches media by the session's tenant,
// which would be the wrong company for a conversation opened through the Hub. A placeholder says an attachment exists.
function Bubble({ m }: { m: HubMessage }) {
  const outbound = m.direction === 'outbound';
  const attachment = m.message_type !== 'text' ? `${TYPE_LABEL[m.message_type] ?? 'Anexo'} (não exibido no Hub)` : '';
  const time = new Date(m.created_at);
  return (
    <li className={clsx('flex', outbound ? 'justify-end' : 'justify-start')}>
      <div
        className={clsx(
          'max-w-[85%] rounded-lg border px-3 py-1.5 text-[13.5px] leading-[1.35] sm:max-w-[75%]',
          outbound ? 'border-accent-primary/10 bg-accent-primary-soft' : 'border-border-subtle bg-surface',
        )}
      >
        {m.body && <p className="whitespace-pre-wrap break-words text-text-primary">{m.body}</p>}
        {attachment && <p className="italic text-text-secondary">{attachment}</p>}
        <p className="mt-0.5 text-right text-[10px] text-text-tertiary">
          <time>{Number.isNaN(time.getTime()) ? '' : time.toLocaleString('pt-BR', { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' })}</time>
          {outbound && <span className="sr-only"> enviada pela equipe</span>}
        </p>
      </div>
    </li>
  );
}

export default function HubItemView({ detail, onBack }: { detail: HubItemDetail; onBack?: () => void }) {
  const end = useRef<HTMLDivElement>(null);
  useEffect(() => {
    end.current?.scrollIntoView?.({ block: 'end' });
  }, [detail.conversation.id, detail.messages.length]);
  return (
    <div className="flex h-full flex-col bg-surface">
      <header className="border-b border-border-subtle px-4 py-2.5">
        <div className="flex items-center gap-3">
          {onBack && (
            <button type="button" onClick={onBack} className="rounded-control p-1 hover:bg-surface-muted md:hidden" aria-label="Voltar para a lista">
              ←
            </button>
          )}
          <div className="min-w-0">
            <h2 className="truncate font-semibold text-text-primary">{detail.item.customer_name || 'Sem nome'}</h2>
            <p className="flex items-center gap-2 text-[11px] text-text-secondary">
              <TenantBadge name={detail.tenant.name || detail.item.tenant_name || 'Empresa não identificada'} />
              <span>· {detail.item.channel || 'Canal não informado'}</span>
              {detail.conversation.status === 'closed' && <span>· Finalizado</span>}
            </p>
          </div>
        </div>
      </header>
      <p role="note" className="border-b border-border-subtle bg-status-info-soft px-4 py-1.5 text-[11px] text-text-secondary">
        Somente leitura. Responder por aqui ainda não está disponível.
      </p>
      <ol aria-label="Mensagens" className="flex-1 space-y-2 overflow-y-auto px-4 py-3">
        {detail.messages.length === 0 && <li className="py-8 text-center text-sm text-text-secondary">Nenhuma mensagem nesta conversa.</li>}
        {detail.messages.map((m) => (
          <Bubble key={m.id} m={m} />
        ))}
        <div ref={end} />
      </ol>
    </div>
  );
}
