import { useEffect, useState } from 'react';
import { useInfiniteQuery } from '@tanstack/react-query';
import { auditLabel, hubAuditAPI, type AccessInstance, type AuditEvent } from '../../lib/hub';
import { handleUnauthorized, isUnauthorized } from '../../lib/session';
import { Button, EmptyState, ErrorState, LoadingState, StatusBadge } from '../primitives';

// "Auditoria" (ADR-0038 §6): who changed what, in the instances of this hub: access, delegation, teams, channels. Read-only. What the server
// sends is a short list of facts per change, never the raw record.
const FACT_LABEL: Record<string, string> = {
  from: 'de', to: 'para', capability: 'capacidade', provider: 'provedor', host: 'servidor', name: 'nome', distribution: 'distribuição',
  members: 'integrantes', instances: 'instâncias', status: 'situação', role: 'papel', can_manage: 'gerenciar', can_reply: 'responder',
  mode: 'acesso', valid_until: 'válido até',
};

function factText(e: AuditEvent): string {
  const facts = Object.entries(e.facts ?? {}).filter(([k]) => k in FACT_LABEL);
  return facts.map(([k, v]) => `${FACT_LABEL[k]}: ${Array.isArray(v) ? v.join(', ') : String(v)}`).join(' · ');
}

export default function AuditPanel({ hubId, instances }: { hubId: string; instances: AccessInstance[] }) {
  const [tenant, setTenant] = useState('');
  const q = useInfiniteQuery({
    queryKey: ['hub-audit', hubId, tenant],
    enabled: !!hubId,
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) => hubAuditAPI.list(hubId, { tenant, before: pageParam }),
    getNextPageParam: (last) => last.next || undefined,
    retry: false,
  });
  useEffect(() => {
    if (isUnauthorized(q.error)) handleUnauthorized();
  }, [q.error]);
  const items = (q.data?.pages ?? []).flatMap((p) => p.items);
  return (
    <section className="space-y-3" aria-label="Auditoria">
      <div className="flex flex-wrap items-center gap-3">
        <p className="max-w-3xl text-sm text-text-secondary">Quem mudou o quê nas instâncias deste Hub: acessos, gestão delegada, equipes e conexões. Mensagens e atendimentos não aparecem aqui.</p>
        <label className="ml-auto flex items-center gap-2 text-sm text-text-secondary">
          Instância
          <select aria-label="Filtrar auditoria por instância" value={tenant} onChange={(e) => setTenant(e.target.value)}
            className="h-9 rounded-control border border-border-light bg-surface px-2 text-sm text-text-primary focus-visible:ring-2 focus-visible:ring-accent-primary">
            <option value="">Todas</option>
            {instances.map((i) => <option key={i.tenant_id} value={i.tenant_id}>{i.name}</option>)}
          </select>
        </label>
      </div>
      {q.isLoading && <LoadingState message="Carregando auditoria…" />}
      {q.isError && !isUnauthorized(q.error) && <ErrorState message="Não foi possível carregar a auditoria." action={{ label: 'Tentar novamente', onClick: () => void q.refetch() }} />}
      {q.data && items.length === 0 && <EmptyState title="Nada registrado" description="Ainda não há mudanças registradas para o filtro escolhido." />}
      {items.length > 0 && (
        <div className="overflow-x-auto rounded-sheet border border-border-subtle bg-surface">
          <table className="w-full min-w-[40rem] text-left text-sm">
            <caption className="sr-only">Mudanças registradas, da mais recente para a mais antiga</caption>
            <thead className="border-b border-border-subtle text-text-secondary">
              <tr><th className="px-3 py-2 font-normal">Quando</th><th className="px-3 py-2 font-normal">Quem</th><th className="px-3 py-2 font-normal">O quê</th><th className="px-3 py-2 font-normal">Instância</th></tr>
            </thead>
            <tbody>
              {items.map((e) => (
                <tr key={e.id} className="border-b border-border-subtle align-top last:border-0">
                  <td className="whitespace-nowrap px-3 py-2 text-text-secondary"><time dateTime={e.at}>{new Date(e.at).toLocaleString('pt-BR')}</time></td>
                  <td className="px-3 py-2">{e.actor_name || e.actor_email || <span className="text-text-tertiary">Sistema</span>}{e.actor_name && e.actor_email && <div className="text-xs text-text-tertiary">{e.actor_email}</div>}</td>
                  <td className="px-3 py-2">
                    <span className="font-medium text-text-primary">{auditLabel(e.action)}</span>{' '}
                    {e.via === 'hub' && <StatusBadge status="info">pelo Hub</StatusBadge>}
                    {factText(e) && <div className="text-xs text-text-tertiary">{factText(e)}</div>}
                  </td>
                  <td className="px-3 py-2 text-text-secondary">{e.tenant_name || '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {q.hasNextPage && <Button variant="secondary" size="sm" isLoading={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>Carregar mais</Button>}
    </section>
  );
}
