import { useMemo } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import axios from 'axios';
import { CollapsibleSection } from '../inbox/CollapsibleSection';
import { ChannelOrigin, channelLabel } from '../inbox/ChannelOrigin';
import { TenantBadge } from '../primitives/TenantBadge';
import { Button } from '../primitives';
import ServiceSessionPanel from '../service/ServiceSessionPanel';
import { TicketPanel } from '../TicketPanel';
import { ContactKindControl } from '../contacts/ContactKindControl';
import { ContactDetailsEditor } from '../contacts/ContactDetailsEditor';
import { classificationAPI, contactsAPI, CONTACT_KIND_LABEL, type ContactKind } from '../../lib/contacts';
import { scopedHeaders, tenantBase, type DelegatedTarget } from '../../lib/acting';
import type { ConversationItem } from '../../types/api';
import type { HubItemDetail } from '../../lib/hub';

const PRIORITY: Record<string, string> = { low: 'Baixa', normal: 'Normal', high: 'Alta', urgent: 'Urgente' };
const OWNER: Record<string, string> = { none: 'Sem responsável', me: 'Com você', other: 'Com outro atendente' };

function when(iso?: string): string {
  if (!iso) return '—';
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? '—' : d.toLocaleString('pt-BR', { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' });
}

interface Props {
  detail: HubItemDetail;
  /** The full workspace of this company may be opened for this person (the server said so; it still decides every request). */
  fullContext: boolean;
  onOpenFull: () => void;
}

// The details of the attendance opened in the unified Hub inbox: the same place and the same collapsible sections as the tenant inbox's context
// pane, filled only with what the Hub item itself carries (company, channel, state, who holds it, the times of the latest messages). Anything that is
// the company's own data (contact card, ERP ticket, notes, history) lives in the full workspace of that company, one click away: this pane never
// reads another company's data with the session's tenant.
export default function HubContextPane({ detail, fullContext, onOpenFull }: Props) {
  const { item, conversation, messages } = detail;
  const tenantName = detail.tenant.name || item.tenant_name || 'Instância não identificada';
  const closed = conversation.status === 'closed';
  // Every delegated key the grant holds inside the contract's ceiling (the server says which; it still decides every request). The panels below are the SAME
  // components the full workspace uses, asked to work on this item's company and Hub explicitly.
  const keys = detail.access.keys ?? detail.access.service_keys ?? [];
  const can = (k: string) => keys.includes(k);
  const hubId = detail.access.hub_id;
  const target: DelegatedTarget | undefined = hubId ? { hubId, tenantId: detail.tenant.id } : undefined;
  const qc = useQueryClient();
  // the cache is per Hub AND per company: what was loaded for one company must never answer for another
  const scope = [hubId, detail.tenant.id, conversation.id] as const;
  const conv = useQuery({
    queryKey: ['hub-conv-detail', ...scope],
    enabled: !!target && can('conversation.read'),
    retry: false,
    queryFn: async () => (await axios.get(`${tenantBase(target)}/inbox/conversations/${conversation.id}`, { headers: scopedHeaders(target) })).data as ConversationItem,
  });
  const contactId = conv.data?.contact_id;
  const card = useQuery({
    queryKey: ['hub-contact', hubId, detail.tenant.id, contactId],
    enabled: !!target && !!contactId && can('contact.read'),
    retry: false,
    queryFn: () => contactsAPI.get(contactId!, target),
  });
  const links = useQuery({
    queryKey: ['hub-contact-links', hubId, detail.tenant.id, contactId],
    enabled: !!target && !!contactId && can('contact.classify'),
    retry: false,
    queryFn: () => classificationAPI.get(contactId!, target),
  });
  const companyNames = [...(links.data?.accounts ?? [])].filter((l) => l.status === 'active').sort((a, b) => Number(b.primary) - Number(a.primary)).map((l) => l.account_name);
  const refreshContact = () => {
    void qc.invalidateQueries({ queryKey: ['hub-conv-detail', ...scope] });
    void qc.invalidateQueries({ queryKey: ['hub-contact', hubId, detail.tenant.id, contactId] });
    void qc.invalidateQueries({ queryKey: ['hub-contact-links', hubId, detail.tenant.id, contactId] });
    void qc.invalidateQueries({ queryKey: ['hub-item', hubId, item.id] });
    void qc.invalidateQueries({ queryKey: ['hub-inbox', hubId] });
  };
  const kind = (card.data?.kind || conv.data?.contact_kind || 'unclassified') as ContactKind;
  const owner = OWNER[conversation.assignment ?? 'none'] ?? OWNER.none;
  // times only: the text of the messages is already on screen in the conversation (and is not repeated here)
  const lastInbound = useMemo(() => [...messages].reverse().find((m) => m.direction === 'inbound'), [messages]);
  const lastOutbound = useMemo(() => [...messages].reverse().find((m) => m.direction === 'outbound'), [messages]);
  return (
    <aside aria-label="Detalhes do atendimento" className="flex h-full min-h-0 flex-col overflow-y-auto bg-surface">
      <header className="border-b border-border-subtle px-4 py-3">
        <h2 className="text-sm font-semibold text-text-primary">Detalhes do atendimento</h2>
        <p className="text-[12px] text-text-secondary">Atendendo {tenantName} pelo Hub</p>
      </header>

      <CollapsibleSection id="hub-attendance" title="Atendimento" icon="conversations" defaultOpen>
        <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1.5 px-4 pb-4 text-[13px]">
          <dt className="text-text-secondary">Empresa</dt>
          <dd className="flex min-w-0 justify-end"><TenantBadge name={tenantName} /></dd>
          <dt className="text-text-secondary">Canal</dt>
          <dd className="flex items-center justify-end gap-1.5 text-text-primary"><ChannelOrigin source={item.channel} />{channelLabel(item.channel)}</dd>
          <dt className="text-text-secondary">Estado</dt>
          <dd className="text-right text-text-primary">{closed ? 'Finalizado' : 'Em atendimento'}</dd>
          <dt className="text-text-secondary">Responsável</dt>
          <dd className="text-right text-text-primary">{owner}</dd>
          <dt className="text-text-secondary">Prioridade</dt>
          <dd className="text-right text-text-primary">{PRIORITY[item.priority] ?? item.priority}</dd>
          {item.unread_count > 0 && (
            <>
              <dt className="text-text-secondary">Sem resposta</dt>
              <dd className="text-right text-text-primary">{item.unread_count}</dd>
            </>
          )}
          <dt className="text-text-secondary">Iniciada</dt>
          <dd className="text-right text-text-primary">{when(conversation.created_at)}</dd>
          <dt className="text-text-secondary">Última atividade</dt>
          <dd className="text-right text-text-primary">{when(item.last_activity_at)}</dd>
        </dl>
      </CollapsibleSection>

      {can('service.read') && target && (
        // ADR-0049: the protocol and its tasks, served through the Hub (the instance named in the URL, the Hub in the acting header; the server proves
        // hub -> contract -> grant -> key on every request, the keys only keep the screen from offering what it would refuse)
        <CollapsibleSection id="hub-service-session" title="Protocolo e tarefas" icon="tickets" defaultOpen>
          <ServiceSessionPanel conversationId={conversation.id} can={can} hub={target} conversationClosed={closed} />
        </CollapsibleSection>
      )}

      <CollapsibleSection id="hub-customer" title="Contato" icon="contacts" defaultOpen>
        <div className="px-4 pb-4">
          <p className="text-[15px] font-semibold text-text-primary">{card.data?.alias || card.data?.display_name || item.customer_name || 'Sem nome'}</p>
          {(card.data?.phone_e164 || conv.data?.contact_phone) && <p className="text-[13px] text-text-secondary">{card.data?.phone_e164 || conv.data?.contact_phone}</p>}
          {card.data?.email && <p className="text-[13px] text-text-secondary">{card.data.email}</p>}
          {conv.data && <p className="mt-1 text-[12px] text-text-secondary">{CONTACT_KIND_LABEL[kind] ?? kind}</p>}
          {!can('contact.read') && !can('contact.classify') && (
            <p className="mt-1 text-[12px] text-text-secondary">Os dados completos do contato não estão liberados para o seu acesso nesta empresa.</p>
          )}
        </div>
        {target && contactId && can('contact.classify') && (
          <section aria-label="Classificar o contato">
            <ContactDetailsEditor contactId={contactId} target={target} onChanged={refreshContact} />
            <ContactKindControl
              contactId={contactId}
              kind={kind}
              internalRole={conv.data?.contact_internal_role}
              contactName={conv.data?.contact_name}
              target={target}
              onChanged={refreshContact}
            />
          </section>
        )}
      </CollapsibleSection>

      {target && (can('ticket.create') || can('ticket.read')) && (
        <CollapsibleSection id="hub-ticket" title="Chamado" icon="tickets" defaultOpen>
          <section aria-label="Chamado no ERP">
            <TicketPanel conversationId={conversation.id} target={target} contactCompanyNames={companyNames} conversationUnassigned={conversation.assignment === 'none'} />
          </section>
        </CollapsibleSection>
      )}

      <CollapsibleSection id="hub-timeline" title="Linha do tempo" icon="tickets" defaultOpen>
        <ol className="space-y-2.5 px-4 pb-4" aria-label="Linha do tempo da conversa">
          <li className="flex items-baseline justify-between gap-3 text-[12px]"><span className="text-text-primary">Conversa iniciada</span><time className="text-text-tertiary">{when(conversation.created_at)}</time></li>
          {lastInbound && <li className="flex items-baseline justify-between gap-3 text-[12px]"><span className="text-text-primary">Última mensagem do cliente</span><time className="text-text-tertiary">{when(lastInbound.created_at)}</time></li>}
          {lastOutbound && <li className="flex items-baseline justify-between gap-3 text-[12px]"><span className="text-text-primary">Última resposta da equipe</span><time className="text-text-tertiary">{when(lastOutbound.created_at)}</time></li>}
          {closed && <li className="text-[12px] text-text-secondary">Atendimento finalizado.</li>}
        </ol>
      </CollapsibleSection>

      <CollapsibleSection id="hub-actions" title="Ações" icon="sparkles" defaultOpen>
        <div className="px-4 pb-4">
          {fullContext ? (
            <Button variant="secondary" size="sm" onClick={onOpenFull}>Abrir na caixa completa de {tenantName}</Button>
          ) : (
            <p className="text-[12px] text-text-secondary">A caixa completa desta empresa não está liberada para você: aqui há conversa, resposta por texto e o que a sua concessão permite.</p>
          )}
        </div>
      </CollapsibleSection>
    </aside>
  );
}
