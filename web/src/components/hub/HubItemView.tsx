import { useEffect, useRef, useState } from 'react';
import clsx from 'clsx';
import { useQueryClient } from '@tanstack/react-query';
import { describeHubWriteError, hubWriteAPI, type HubItemDetail, type HubMessage } from '../../lib/hub';
import { handleUnauthorized, isUnauthorized } from '../../lib/session';
import { TenantBadge } from '../primitives/TenantBadge';
import MessageComposer from '../inbox/MessageComposer';
import TransferDialog from './TransferDialog';
import { ChannelOrigin, channelLabel } from '../inbox/ChannelOrigin';

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

// What the composer area shows, decided from what the SERVER said about this conversation and this grant.
function composerMode(d: HubItemDetail): 'readonly' | 'closed' | 'claim' | 'other' | 'reply' {
  if (!d.access.can_reply) return 'readonly';
  if (d.conversation.status === 'closed') return 'closed';
  if (d.conversation.assignment === 'me') return 'reply';
  return d.conversation.assignment === 'other' ? 'other' : 'claim';
}

export default function HubItemView({ hubId, detail, onBack }: { hubId: string; detail: HubItemDetail; onBack?: () => void }) {
  const end = useRef<HTMLDivElement>(null);
  const queryClient = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [transferring, setTransferring] = useState(false);
  // A retry of the SAME text keeps its key (no double send); a different text is a new attempt.
  const pending = useRef<{ text: string; key: string } | null>(null);
  const tenantName = detail.tenant.name || detail.item.tenant_name || 'Instância não identificada';
  const mode = composerMode(detail);
  useEffect(() => {
    setError(null);
    pending.current = null;
  }, [detail.item.id]);
  const refresh = () => queryClient.invalidateQueries({ queryKey: ['hub-item', hubId, detail.item.id] });
  const fail = (err: unknown) => {
    if (isUnauthorized(err)) handleUnauthorized();
    else setError(describeHubWriteError(err));
    // the screen may be stale (someone else took it, access ended): show the truth
    void refresh();
  };
  const claim = async () => {
    setBusy(true);
    setError(null);
    try {
      await hubWriteAPI.claim(hubId, detail.item.id, detail.tenant.id);
      await refresh();
    } catch (err) {
      fail(err);
    } finally {
      setBusy(false);
    }
  };
  const send = async (text: string): Promise<boolean> => {
    if (!text.trim()) return false;
    setBusy(true);
    setError(null);
    if (pending.current?.text !== text) pending.current = { text, key: crypto.randomUUID() };
    try {
      await hubWriteAPI.reply(hubId, detail.item.id, detail.tenant.id, text, pending.current.key);
      pending.current = null;
      await refresh();
      return true;
    } catch (err) {
      fail(err);
      return false;
    } finally {
      setBusy(false);
    }
  };
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
              <TenantBadge name={detail.tenant.name || detail.item.tenant_name || 'Instância não identificada'} />
              <ChannelOrigin source={detail.item.channel} />
              <span>{channelLabel(detail.item.channel)}</span>
              {detail.conversation.status === 'closed' && <span>· Finalizado</span>}
            </p>
          </div>
        </div>
      </header>
      <ol aria-label="Mensagens" className="flex-1 space-y-2 overflow-y-auto px-4 py-3">
        {detail.messages.length === 0 && <li className="py-8 text-center text-sm text-text-secondary">Nenhuma mensagem nesta conversa.</li>}
        {detail.messages.map((m) => (
          <Bubble key={m.id} m={m} />
        ))}
        <div ref={end} />
      </ol>
      <footer className="border-t border-border-subtle px-4 py-3">
        {error && (
          <div role="alert" className="mb-2 rounded-control bg-status-danger-soft px-2 py-1.5 text-xs text-status-danger">
            {error}
          </div>
        )}
        {mode === 'readonly' && (
          <p role="note" className="text-[12px] text-text-secondary">Somente leitura: seu acesso a {tenantName} não permite responder.</p>
        )}
        {mode === 'closed' && <p role="note" className="text-[12px] text-text-secondary">Atendimento finalizado.</p>}
        {mode === 'other' && <p role="note" className="text-[12px] text-text-secondary">Esta conversa está com outro operador.</p>}
        {mode === 'claim' && (
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="text-[12px] text-text-secondary">Assuma a conversa para responder como {tenantName}.</p>
            <button type="button" onClick={() => void claim()} disabled={busy} className="h-9 rounded-control bg-accent-primary px-3 text-sm font-medium text-white disabled:opacity-50">
              Assumir
            </button>
          </div>
        )}
        {mode === 'reply' && (
          <div className="mb-1 flex justify-end">
            <button type="button" onClick={() => setTransferring(true)} className="text-xs font-medium text-accent-primary underline-offset-2 hover:underline">Transferir conversa</button>
          </div>
        )}
        {mode === 'reply' && (
          <MessageComposer
            draftKey={`hub:${detail.tenant.id}:${detail.conversation.id}`}
            onSend={send}
            disabled={busy}
            label={`Respondendo como ${tenantName}`}
            labelRight={detail.item.channel || undefined}
          />
        )}
      </footer>
      <TransferDialog hubId={hubId} detail={detail} open={transferring} onClose={() => setTransferring(false)}
        onDone={() => { setTransferring(false); void refresh(); void queryClient.invalidateQueries({ queryKey: ['hub-inbox', hubId] }); }} />
    </div>
  );
}
