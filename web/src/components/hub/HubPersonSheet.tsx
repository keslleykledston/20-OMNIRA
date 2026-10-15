import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Button, Drawer, StatusBadge } from '../primitives';
import { describeHubPoolError, hubPoolsAPI, type AccessAgent, type AccessInstance, type AccessMode, type WorkPool } from '../../lib/hub';

const MODE_LABEL: Record<AccessMode, string> = { none: 'Sem acesso', read: 'Só leitura', reply: 'Ler e responder' };
const POOL_CAPACITY = 10; // the same starting capacity as the Equipes tab

// ADR-0047: one person, every instance of the Hub on one sheet: the access to each one and the teams (Equipes) through which they receive
// its conversations. Same APIs as the matrix and the Equipes tab; the server decides every change. A direct member's function and queues
// stay with that instance's administrator (Pessoas, inside the instance).
export default function HubPersonSheet({ hubId, agent, instances, busy, onSetAccess, onSetManage, onRemove, onClose }: {
  hubId: string;
  agent: AccessAgent;
  instances: AccessInstance[];
  busy: boolean;
  onSetAccess: (user: string, tenant: string, mode: AccessMode) => void;
  onSetManage: (user: string, tenant: string, can: boolean) => void;
  onRemove: (agent: AccessAgent) => void;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const pools = useQuery({ queryKey: ['hub-pools', hubId], enabled: !!hubId, queryFn: () => hubPoolsAPI.list(hubId), retry: false });
  const [poolBusy, setPoolBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const who = agent.name || agent.email || 'Sem nome';
  const grant = new Map(agent.grants.map((g) => [g.tenant_id, g]));
  const nameOf = new Map(instances.map((i) => [i.tenant_id, i.name]));

  // The Equipes API replaces the whole member list: read it again right before writing, so a change made meanwhile is kept.
  const togglePool = async (pool: WorkPool, on: boolean) => {
    setPoolBusy(pool.id);
    setError(null);
    try {
      const fresh = (await hubPoolsAPI.list(hubId)).find((p) => p.id === pool.id);
      if (!fresh) throw new Error('missing');
      const others = fresh.members.filter((m) => m.user_id !== agent.user_id).map((m) => ({ user_id: m.user_id, max_open: m.max_open }));
      await hubPoolsAPI.setMembers(hubId, pool.id, on ? [...others, { user_id: agent.user_id, max_open: POOL_CAPACITY }] : others);
    } catch (err) {
      setError(describeHubPoolError(err));
    } finally {
      setPoolBusy(null);
      await qc.invalidateQueries({ queryKey: ['hub-pools', hubId] });
    }
  };

  return (
    <Drawer open title={`Ficha de ${who}`} onClose={onClose} size="lg">
      <div className="px-5 pb-8">
        <header className="space-y-1 pb-4 pr-10 pt-5">
          <h2 className="truncate text-lg font-semibold text-text-primary">{who}</h2>
          {agent.name && <p className="truncate text-sm text-text-secondary">{agent.email}</p>}
          <div className="flex flex-wrap items-center gap-2 pt-1">
            {agent.hub_role === 'hub_admin' && <StatusBadge status="info">Admin do Hub</StatusBadge>}
            <StatusBadge status={agent.instances > 1 ? 'warning' : 'default'}>
              {agent.instances === 0 ? 'Nenhuma instância' : agent.instances === 1 ? 'Uma instância' : `${agent.instances} instâncias`}
            </StatusBadge>
          </div>
        </header>
        {error && <p role="alert" className="mb-3 rounded-control border border-status-danger-border bg-status-danger-soft px-3 py-2 text-sm text-status-danger">{error}</p>}

        <ul className="space-y-3" aria-label={`Instâncias de ${who}`}>
          {instances.map((i) => {
            const g = grant.get(i.tenant_id);
            const mode: AccessMode = g?.mode ?? 'none';
            const locked = i.tenant_status !== 'active' || i.contract_status !== 'active';
            const direct = agent.direct_instances.includes(i.tenant_id);
            const covering = (pools.data ?? []).filter((p) => p.instances.some((x) => x.tenant_id === i.tenant_id));
            return (
              <li key={i.tenant_id} className="space-y-3 rounded-card border border-border-subtle p-4" aria-label={`${i.name} — ${who}`}>
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <h3 className="text-sm font-semibold text-text-primary">
                    {i.name} {locked && <span className="text-xs font-normal text-status-danger">· suspensa</span>}
                  </h3>
                  <select
                    aria-label={`Acesso de ${who} em ${i.name}`}
                    value={mode}
                    disabled={busy || locked}
                    onChange={(e) => onSetAccess(agent.user_id, i.tenant_id, e.target.value as AccessMode)}
                    className="h-9 rounded-control border border-border-light bg-surface px-2 text-sm text-text-primary focus-visible:ring-2 focus-visible:ring-accent-primary"
                  >
                    {(Object.keys(MODE_LABEL) as AccessMode[]).map((m) => <option key={m} value={m}>{MODE_LABEL[m]}</option>)}
                  </select>
                </div>
                {direct && (
                  <p className="text-xs text-text-secondary">
                    Também é membro direto desta instância: a função e as filas de lá são definidas pelo administrador dela, em Pessoas.
                  </p>
                )}
                {mode !== 'none' && (i.management_scopes ?? []).length > 0 && !locked && (
                  <label className="flex items-center gap-2 text-sm text-text-secondary">
                    <input type="checkbox" checked={!!g?.can_manage} disabled={busy}
                      onChange={(e) => onSetManage(agent.user_id, i.tenant_id, e.target.checked)}
                      aria-label={`Gerenciar canais e integrações de ${i.name} — ${who}`} />
                    Gerenciar canais e integrações
                  </label>
                )}
                <div className="space-y-2">
                  <p className="text-xs font-semibold uppercase tracking-wide text-text-tertiary">Recebe conversas de</p>
                  {mode !== 'reply' ? (
                    <p className="text-sm text-text-secondary">Só com “Ler e responder” a pessoa recebe conversas desta instância.</p>
                  ) : pools.isLoading ? (
                    <p className="text-sm text-text-secondary">Carregando equipes…</p>
                  ) : covering.length === 0 ? (
                    <p className="text-sm text-text-secondary">Nenhuma equipe atende esta instância. Crie uma na aba Equipes.</p>
                  ) : (
                    covering.map((p) => {
                      const member = p.members.some((m) => m.user_id === agent.user_id);
                      const scope = p.instances.find((x) => x.tenant_id === i.tenant_id)?.queue_id ? 'uma fila específica' : 'todas as filas';
                      const elsewhere = p.instances.filter((x) => x.tenant_id !== i.tenant_id).map((x) => nameOf.get(x.tenant_id) ?? x.name);
                      return (
                        <label key={p.id} className="flex items-start gap-3 rounded-control border border-border-subtle p-3 text-sm">
                          <input type="checkbox" checked={member} disabled={poolBusy === p.id || locked}
                            onChange={(e) => void togglePool(p, e.target.checked)}
                            aria-label={`${who} na equipe ${p.name}`} className="mt-0.5 h-4 w-4 accent-[var(--color-accent-primary)]" />
                          <span className="min-w-0">
                            <span className="block font-medium text-text-primary">Equipe {p.name}</span>
                            <span className="block text-xs text-text-tertiary">
                              {scope} desta instância{elsewhere.length > 0 ? ` · também atende ${elsewhere.join(', ')}` : ''}
                            </span>
                          </span>
                        </label>
                      );
                    })
                  )}
                </div>
              </li>
            );
          })}
        </ul>

        {agent.hub_role !== 'hub_admin' && (
          <div className="mt-6 flex flex-wrap items-center justify-between gap-2 border-t border-border-subtle pt-4 text-sm">
            <span className="text-text-secondary">Tirar a pessoa do Hub encerra todos os acessos dela às instâncias.</span>
            <Button size="sm" variant="danger" onClick={() => onRemove(agent)}>Remover do Hub</Button>
          </div>
        )}
      </div>
    </Drawer>
  );
}
