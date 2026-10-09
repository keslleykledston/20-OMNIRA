import { useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  describeHubPoolError, hubPoolsAPI,
  type AccessAgent, type AccessInstance, type PoolDistribution, type WorkPool,
} from '../../lib/hub';
import { handleUnauthorized, isUnauthorized } from '../../lib/session';
import { Button, ConfirmDialog, EmptyState, ErrorState, Input, LoadingState } from '../primitives';

// "Equipes" (ADR-0038 phase 4): groups of the hub's agents, the instances each group answers for, each person's capacity and whether
// a new conversation is handed out by itself. A team grants nothing: distribution only chooses among people who already hold a live
// reply grant on the instance, and the server proves that again for the person chosen. Everything here is decided by the server.
const DISTRIBUTION_LABEL: Record<PoolDistribution, string> = {
  manual: 'Manual: cada pessoa assume a conversa',
  round_robin: 'Automática: a conversa nova vai para quem está com menos conversas',
};

export default function PoolsPanel({ hubId, agents, instances }: { hubId: string; agents: AccessAgent[]; instances: AccessInstance[] }) {
  const qc = useQueryClient();
  const [notice, setNotice] = useState<{ tone: 'ok' | 'error'; text: string } | null>(null);
  const [newName, setNewName] = useState('');
  const [removing, setRemoving] = useState<WorkPool | null>(null);
  const list = useQuery({ queryKey: ['hub-pools', hubId], enabled: !!hubId, queryFn: () => hubPoolsAPI.list(hubId), retry: false });
  useEffect(() => {
    if (isUnauthorized(list.error)) handleUnauthorized();
  }, [list.error]);

  const refresh = () => qc.invalidateQueries({ queryKey: ['hub-pools', hubId] });
  const done = (text?: string) => { setNotice(text ? { tone: 'ok', text } : null); void refresh(); };
  const fail = (err: unknown) => { setNotice({ tone: 'error', text: describeHubPoolError(err) }); void refresh(); };
  const create = useMutation({
    mutationFn: () => hubPoolsAPI.create(hubId, newName.trim(), 'manual'),
    onSuccess: () => { setNewName(''); done('Equipe criada. Agora escolha quem faz parte e quais instâncias ela atende.'); },
    onError: fail,
  });
  const remove = useMutation({
    mutationFn: (id: string) => hubPoolsAPI.remove(hubId, id),
    onSuccess: () => done('Equipe removida.'),
    onError: fail,
    onSettled: () => setRemoving(null),
  });

  const pools = list.data ?? [];
  return (
    <section className="space-y-4" aria-label="Equipes">
      <p className="max-w-3xl text-sm text-text-secondary">
        Uma equipe junta agentes do Hub e diz quais instâncias ela atende. Na distribuição automática, a conversa nova vai para quem tem menos conversas abertas e ainda pode responder aquela instância. A equipe não dá acesso a ninguém: o acesso continua sendo o da aba Agentes e permissões.
      </p>
      <form className="flex flex-wrap items-end gap-2 rounded-sheet border border-border-subtle bg-surface p-4" aria-label="Nova equipe"
        onSubmit={(e) => { e.preventDefault(); if (newName.trim() && !create.isPending) create.mutate(); }}>
        <div className="min-w-[14rem] flex-1">
          <Input label="Nova equipe" value={newName} onChange={(e) => setNewName(e.target.value)} maxLength={200} placeholder="Ex.: Suporte nível 1" />
        </div>
        <Button type="submit" disabled={!newName.trim()} isLoading={create.isPending}>Criar equipe</Button>
      </form>
      {notice && (
        <div role="alert" className={notice.tone === 'error' ? 'rounded-control border border-status-danger-border bg-status-danger-soft px-3 py-2 text-sm text-status-danger' : 'rounded-control border border-status-success-border bg-status-success-soft px-3 py-2 text-sm text-status-success'}>
          {notice.text}
        </div>
      )}
      {list.isLoading && <LoadingState message="Carregando equipes…" />}
      {list.isError && !isUnauthorized(list.error) && <ErrorState message={describeHubPoolError(list.error)} action={{ label: 'Tentar novamente', onClick: () => void list.refetch() }} />}
      {list.data && pools.length === 0 && <EmptyState title="Nenhuma equipe" description="Sem equipes, ninguém recebe conversas automaticamente: cada pessoa assume a sua." />}
      {pools.map((p) => (
        <PoolCard key={p.id} pool={p} hubId={hubId} agents={agents} instances={instances} onSaved={done} onFailed={fail} onRemove={() => setRemoving(p)} />
      ))}
      <ConfirmDialog open={!!removing} title={`Remover a equipe ${removing?.name ?? ''}?`} destructive isPending={remove.isPending} confirmLabel="Remover equipe"
        message="As conversas que já estão com alguém continuam com essa pessoa. Novas conversas dessas instâncias deixam de ser distribuídas por esta equipe."
        onCancel={() => setRemoving(null)} onConfirm={() => removing && remove.mutate(removing.id)} />
    </section>
  );
}

function PoolCard({ pool, hubId, agents, instances, onSaved, onFailed, onRemove }: {
  pool: WorkPool; hubId: string; agents: AccessAgent[]; instances: AccessInstance[];
  onSaved: (text?: string) => void; onFailed: (err: unknown) => void; onRemove: () => void;
}) {
  // what the server last said; the form below edits a copy until "Salvar"
  const [members, setMembers] = useState<Record<string, number>>(() => Object.fromEntries(pool.members.map((m) => [m.user_id, m.max_open])));
  const [tenants, setTenants] = useState<string[]>(() => pool.instances.filter((i) => !i.queue_id).map((i) => i.tenant_id));
  useEffect(() => {
    setMembers(Object.fromEntries(pool.members.map((m) => [m.user_id, m.max_open])));
    setTenants(pool.instances.filter((i) => !i.queue_id).map((i) => i.tenant_id));
  }, [pool.members, pool.instances]);
  const queueScoped = pool.instances.filter((i) => i.queue_id);
  const loadOf = new Map(pool.members.map((m) => [m.user_id, m.load]));

  const setDistribution = useMutation({
    mutationFn: (d: PoolDistribution) => hubPoolsAPI.update(hubId, pool.id, { distribution: d }),
    onSuccess: () => onSaved(),
    onError: onFailed,
  });
  const saveMembers = useMutation({
    mutationFn: () => hubPoolsAPI.setMembers(hubId, pool.id, Object.entries(members).map(([user_id, max_open]) => ({ user_id, max_open }))),
    onSuccess: () => onSaved('Integrantes salvos.'),
    onError: onFailed,
  });
  const saveInstances = useMutation({
    mutationFn: () => hubPoolsAPI.setInstances(hubId, pool.id, [
      ...tenants.map((tenant_id) => ({ tenant_id })),
      ...queueScoped.map((i) => ({ tenant_id: i.tenant_id, queue_id: i.queue_id })), // queue-level entries are kept as they are
    ]),
    onSuccess: () => onSaved('Instâncias salvas.'),
    onError: onFailed,
  });

  return (
    <article aria-label={`Equipe ${pool.name}`} className="space-y-4 rounded-sheet border border-border-subtle bg-surface p-4">
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="text-base font-semibold text-text-primary">{pool.name}</h2>
        <label className="flex items-center gap-2 text-sm text-text-secondary">
          <span className="sr-only">Distribuição da equipe {pool.name}</span>
          <select aria-label={`Distribuição da equipe ${pool.name}`} value={pool.distribution} disabled={setDistribution.isPending}
            onChange={(e) => setDistribution.mutate(e.target.value as PoolDistribution)}
            className="h-9 rounded-control border border-border-light bg-surface px-2 text-sm text-text-primary focus-visible:ring-2 focus-visible:ring-accent-primary">
            {(Object.keys(DISTRIBUTION_LABEL) as PoolDistribution[]).map((d) => <option key={d} value={d}>{DISTRIBUTION_LABEL[d]}</option>)}
          </select>
        </label>
        <button type="button" className="ml-auto text-xs text-status-danger underline-offset-2 hover:underline" onClick={onRemove}>Remover equipe</button>
      </div>

      <fieldset className="space-y-2">
        <legend className="text-sm font-medium text-text-primary">Integrantes e capacidade</legend>
        <p className="text-xs text-text-tertiary">Capacidade = quantas conversas abertas a pessoa pode ter ao mesmo tempo antes de a distribuição pular para a próxima.</p>
        {agents.length === 0 && <p className="text-sm text-text-secondary">O Hub ainda não tem agentes.</p>}
        <ul className="divide-y divide-border-subtle">
          {agents.map((a) => {
            const on = a.user_id in members;
            return (
              <li key={a.user_id} className="flex flex-wrap items-center gap-3 py-1.5 text-sm">
                <label className="flex items-center gap-2">
                  <input type="checkbox" checked={on} aria-label={`${a.email || a.name} na equipe ${pool.name}`}
                    onChange={(e) => setMembers((m) => { const n = { ...m }; if (e.target.checked) n[a.user_id] = 10; else delete n[a.user_id]; return n; })} />
                  <span className="font-medium text-text-primary">{a.name || a.email}</span>
                  {a.name && <span className="text-text-secondary">{a.email}</span>}
                </label>
                {on && (
                  <>
                    <label className="ml-auto flex items-center gap-1.5 text-xs text-text-secondary">
                      Capacidade
                      <input type="number" min={1} max={500} value={members[a.user_id]} aria-label={`Capacidade de ${a.email || a.name} na equipe ${pool.name}`}
                        onChange={(e) => setMembers((m) => ({ ...m, [a.user_id]: Math.max(1, Math.min(500, Number(e.target.value) || 1)) }))}
                        className="h-8 w-20 rounded-control border border-border-light bg-surface px-2 text-sm text-text-primary" />
                    </label>
                    <span className="text-xs text-text-tertiary">{loadOf.get(a.user_id) ?? 0} abertas</span>
                  </>
                )}
              </li>
            );
          })}
        </ul>
        <Button size="sm" onClick={() => saveMembers.mutate()} isLoading={saveMembers.isPending}>Salvar integrantes</Button>
      </fieldset>

      <fieldset className="space-y-2">
        <legend className="text-sm font-medium text-text-primary">Instâncias que a equipe atende</legend>
        <div className="flex flex-wrap gap-4">
          {instances.map((i) => (
            <label key={i.tenant_id} className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={tenants.includes(i.tenant_id)} aria-label={`${i.name} atendida pela equipe ${pool.name}`}
                onChange={(e) => setTenants((t) => (e.target.checked ? [...t, i.tenant_id] : t.filter((x) => x !== i.tenant_id)))} />
              {i.name}
            </label>
          ))}
        </div>
        {queueScoped.length > 0 && <p className="text-xs text-text-tertiary">{queueScoped.length} atendimento(s) por fila foram configurados fora desta tela e ficam como estão.</p>}
        <Button size="sm" onClick={() => saveInstances.mutate()} isLoading={saveInstances.isPending}>Salvar instâncias</Button>
      </fieldset>
    </article>
  );
}
