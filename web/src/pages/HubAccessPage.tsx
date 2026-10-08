import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  describeHubAccessError, hubAccessAPI,
  type AccessAgent, type AccessInstance, type AccessMode, type AccessOverview, type AccessPerson,
} from '../lib/hub';
import { handleUnauthorized, isUnauthorized } from '../lib/session';
import { useMyHubs } from '../hooks/useMyHubs';
import { Button, ConfirmDialog, EmptyState, ErrorState, Input, LoadingState, StatusBadge } from '../components/primitives';

// Access panel (ADR-0039): who administers each instance (company), which agents exist and what each one may do where.
// Separate from the conversations workspace on purpose. Everything shown and done is decided by the server for the
// signed-in hub admin; `can_manage_access` only decides whether the screen is offered. Only an admin of the HUB can
// authorize an agent for more than one instance: a company's own administrator manages their company's people in
// "Equipe" and has no route into this panel.
type Tab = 'instances' | 'agents';

const MODE_LABEL: Record<AccessMode, string> = { none: 'Sem acesso', read: 'Só leitura', reply: 'Ler e responder' };

export function instanceLabel(n: number): string {
  if (n <= 0) return 'Nenhuma instância';
  return n === 1 ? 'Uma instância' : `${n} instâncias`;
}

export default function HubAccessPage() {
  const hubs = useMyHubs();
  const hub = (hubs.data ?? []).find((h) => h.can_manage_access);
  const hubId = hub?.id ?? '';
  const qc = useQueryClient();
  const [tab, setTab] = useState<Tab>('agents');
  const [notice, setNotice] = useState<{ tone: 'ok' | 'error'; text: string } | null>(null);
  const [removeAgent, setRemoveAgent] = useState<AccessAgent | null>(null);
  const [removeAdmin, setRemoveAdmin] = useState<{ instance: AccessInstance; person: AccessPerson } | null>(null);

  const overview = useQuery({ queryKey: ['hub-access', hubId], enabled: !!hubId, queryFn: () => hubAccessAPI.overview(hubId), retry: false });
  useEffect(() => {
    if ([hubs.error, overview.error].some(isUnauthorized)) handleUnauthorized();
  }, [hubs.error, overview.error]);

  const refresh = () => qc.invalidateQueries({ queryKey: ['hub-access', hubId] });
  const fail = (err: unknown) => { setNotice({ tone: 'error', text: describeHubAccessError(err) }); void refresh(); };
  const ok = (text: string) => { setNotice({ tone: 'ok', text }); void refresh(); };

  const setAccess = useMutation({
    mutationFn: (v: { user: string; tenant: string; mode: AccessMode }) => hubAccessAPI.setAccess(hubId, v.user, v.tenant, v.mode),
    onSuccess: () => { setNotice(null); void refresh(); },
    onError: fail,
  });
  const addAgent = useMutation({
    mutationFn: (email: string) => hubAccessAPI.addAgent(hubId, email),
    onSuccess: (p) => ok(`${p.email || 'A pessoa'} agora é agente deste Hub. Ela ainda não tem acesso a nenhuma instância: defina na tabela.`),
    onError: fail,
  });
  const dropAgent = useMutation({
    mutationFn: (userId: string) => hubAccessAPI.removeAgent(hubId, userId),
    onSuccess: () => ok('Agente removido do Hub. Os acessos dele às instâncias terminaram.'),
    onError: fail,
    onSettled: () => setRemoveAgent(null),
  });
  const addAdmin = useMutation({
    mutationFn: (v: { tenant: string; email: string }) => hubAccessAPI.addAdmin(hubId, v.tenant, v.email),
    onSuccess: (p) => ok(`${p.email || 'A pessoa'} agora administra a instância.`),
    onError: fail,
  });
  const dropAdmin = useMutation({
    mutationFn: (v: { tenant: string; user: string }) => hubAccessAPI.removeAdmin(hubId, v.tenant, v.user),
    onSuccess: () => ok('Administração retirada. A pessoa continua na empresa como atendente.'),
    onError: fail,
    onSettled: () => setRemoveAdmin(null),
  });

  if (hubs.isLoading) return <div className="p-6"><LoadingState message="Carregando…" /></div>;
  if (!hub) {
    return (
      <div className="p-6">
        <EmptyState title="Painel de acessos indisponível" description="Esta área é só para administradores de um Hub, e precisa estar habilitada neste ambiente." />
      </div>
    );
  }
  const data = overview.data;

  return (
    <div className="flex h-full min-h-0 flex-col overflow-y-auto">
      <div className="flex flex-wrap items-center gap-3 border-b border-border-subtle px-4 py-3">
        <Link to="/hub" className="text-sm text-text-secondary underline-offset-2 hover:underline">← Caixa do Hub</Link>
        <h1 className="text-lg font-semibold text-text-primary">Acessos</h1>
        <span className="text-sm text-text-secondary">{hub.name}</span>
        {hub.can_manage_companies && <Link to="/hub/empresas" className="ml-auto text-sm font-medium text-accent-primary underline-offset-2 hover:underline">Empresas</Link>}
      </div>
      <div className="mx-auto w-full max-w-6xl space-y-4 p-4">
        <div role="tablist" aria-label="Seções do painel de acessos" className="inline-flex rounded-control bg-surface-muted p-1">
          {([['agents', 'Agentes e permissões'], ['instances', 'Instâncias e administradores']] as const).map(([id, label]) => (
            <button key={id} role="tab" type="button" aria-selected={tab === id} onClick={() => setTab(id)}
              className={`h-8 rounded-control px-3 text-sm font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary ${tab === id ? 'bg-surface text-text-primary shadow-sm' : 'text-text-secondary hover:text-text-primary'}`}>
              {label}
            </button>
          ))}
        </div>
        {notice && (
          <div role="alert" className={notice.tone === 'error' ? 'rounded-control border border-status-danger-border bg-status-danger-soft px-3 py-2 text-sm text-status-danger' : 'rounded-control border border-status-success-border bg-status-success-soft px-3 py-2 text-sm text-status-success'}>
            {notice.text}
          </div>
        )}
        {overview.isLoading && <LoadingState message="Carregando acessos…" />}
        {overview.isError && !isUnauthorized(overview.error) && <ErrorState message={describeHubAccessError(overview.error)} action={{ label: 'Tentar novamente', onClick: () => void overview.refetch() }} />}
        {data && tab === 'agents' && (
          <AgentsTab data={data} busy={setAccess.isPending} adding={addAgent.isPending}
            onSet={(user, tenant, mode) => setAccess.mutate({ user, tenant, mode })}
            onAdd={(email) => addAgent.mutate(email)}
            onRemove={(a) => setRemoveAgent(a)} />
        )}
        {data && tab === 'instances' && (
          <InstancesTab data={data} adding={addAdmin.isPending}
            onAdd={(tenant, email) => addAdmin.mutate({ tenant, email })}
            onRemove={(instance, person) => setRemoveAdmin({ instance, person })} />
        )}
      </div>
      <ConfirmDialog open={!!removeAgent} title={`Remover ${removeAgent?.email || 'agente'} do Hub?`} destructive isPending={dropAgent.isPending} confirmLabel="Remover agente"
        message="Todos os acessos deste agente às instâncias deste Hub terminam na hora. A conta dele não é apagada."
        onCancel={() => setRemoveAgent(null)} onConfirm={() => removeAgent && dropAgent.mutate(removeAgent.user_id)} />
      <ConfirmDialog open={!!removeAdmin} title={`Retirar a administração de ${removeAdmin?.person.email || 'esta pessoa'}?`} destructive isPending={dropAdmin.isPending} confirmLabel="Retirar administração"
        message={`Ela deixa de administrar ${removeAdmin?.instance.name ?? 'a instância'}, mas continua na empresa como atendente. Uma instância precisa de ao menos um administrador.`}
        onCancel={() => setRemoveAdmin(null)} onConfirm={() => removeAdmin && dropAdmin.mutate({ tenant: removeAdmin.instance.tenant_id, user: removeAdmin.person.user_id })} />
    </div>
  );
}

function AddByEmail({ label, placeholder, help, busy, onAdd }: { label: string; placeholder: string; help?: string; busy: boolean; onAdd: (email: string) => void }) {
  const [email, setEmail] = useState('');
  const valid = /^\S+@\S+$/.test(email.trim());
  return (
    <form className="flex flex-wrap items-end gap-2" onSubmit={(e) => { e.preventDefault(); if (valid && !busy) { onAdd(email.trim()); setEmail(''); } }}>
      <div className="min-w-[16rem] flex-1">
        <Input label={label} type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder={placeholder} maxLength={254} helperText={help} />
      </div>
      <Button size="sm" type="submit" disabled={!valid} isLoading={busy}>Adicionar</Button>
    </form>
  );
}

function AgentsTab({ data, busy, adding, onSet, onAdd, onRemove }: {
  data: AccessOverview; busy: boolean; adding: boolean;
  onSet: (user: string, tenant: string, mode: AccessMode) => void;
  onAdd: (email: string) => void;
  onRemove: (a: AccessAgent) => void;
}) {
  const instances = data.instances;
  const nameOf = new Map(instances.map((i) => [i.tenant_id, i.name]));
  return (
    <section className="space-y-4" aria-label="Agentes e permissões">
      <div className="rounded-sheet border border-border-subtle bg-surface p-4">
        <AddByEmail label="Adicionar agente ao Hub" placeholder="pessoa@empresa.com.br" busy={adding} onAdd={onAdd}
          help="A pessoa precisa já ter conta no OMNIRA. Entrar no Hub não dá acesso a nenhuma instância: o acesso é definido na tabela abaixo." />
      </div>
      {data.agents.length === 0 && <EmptyState title="Nenhum agente neste Hub" description="Adicione um agente e depois defina em quais instâncias ele atua." />}
      {data.agents.length > 0 && instances.length === 0 && <EmptyState title="Nenhuma instância" description="Crie uma empresa em Empresas para poder liberar agentes." />}
      {data.agents.length > 0 && instances.length > 0 && (
        <div className="overflow-x-auto rounded-sheet border border-border-subtle bg-surface">
          <table className="w-full min-w-[40rem] text-sm">
            <caption className="sr-only">Acesso de cada agente a cada instância</caption>
            <thead>
              <tr className="border-b border-border-subtle text-left text-text-secondary">
                <th scope="col" className="px-3 py-2 font-medium">Agente</th>
                {instances.map((i) => (
                  <th key={i.tenant_id} scope="col" className="px-3 py-2 font-medium">
                    {i.name}
                    {i.tenant_status !== 'active' || i.contract_status !== 'active' ? <span className="block text-xs font-normal text-status-danger">Suspensa</span> : null}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {data.agents.map((a) => {
                const cell = new Map(a.grants.map((g) => [g.tenant_id, g.mode]));
                const direct = a.direct_instances.map((t) => nameOf.get(t) ?? 'outra empresa');
                return (
                  <tr key={a.user_id} className="border-b border-border-subtle last:border-0 align-top">
                    <th scope="row" className="px-3 py-2 text-left font-normal">
                      <div className="font-medium text-text-primary">{a.name || a.email || 'Sem nome'}</div>
                      {a.name && <div className="text-text-secondary">{a.email}</div>}
                      <div className="mt-1 flex flex-wrap items-center gap-1.5">
                        {a.hub_role === 'hub_admin' && <StatusBadge status="info">Admin do Hub</StatusBadge>}
                        <StatusBadge status={a.instances > 1 ? 'warning' : 'default'}>{instanceLabel(a.instances)}</StatusBadge>
                        {a.hub_role !== 'hub_admin' && (
                          <button type="button" className="text-xs text-status-danger underline-offset-2 hover:underline" onClick={() => onRemove(a)}>Remover do Hub</button>
                        )}
                      </div>
                      {direct.length > 0 && <div className="mt-1 text-xs text-text-tertiary">Também é membro direto de: {direct.join(', ')}</div>}
                    </th>
                    {instances.map((i) => {
                      const value: AccessMode = cell.get(i.tenant_id) ?? 'none';
                      const locked = i.tenant_status !== 'active' || i.contract_status !== 'active';
                      return (
                        <td key={i.tenant_id} className="px-3 py-2">
                          <select aria-label={`Acesso de ${a.email || a.name} em ${i.name}`} value={value} disabled={busy || locked}
                            onChange={(e) => onSet(a.user_id, i.tenant_id, e.target.value as AccessMode)}
                            className="h-9 w-full rounded-control border border-border-light bg-surface px-2 text-sm text-text-primary focus-visible:ring-2 focus-visible:ring-accent-primary">
                            {(Object.keys(MODE_LABEL) as AccessMode[]).map((m) => <option key={m} value={m}>{MODE_LABEL[m]}</option>)}
                          </select>
                        </td>
                      );
                    })}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      <p className="text-xs text-text-tertiary">
        Só o administrador do Hub libera um agente em mais de uma instância. O administrador de cada empresa gerencia as pessoas dela em “Equipe”, dentro do OMNIRA.
      </p>
    </section>
  );
}

function InstancesTab({ data, adding, onAdd, onRemove }: {
  data: AccessOverview; adding: boolean;
  onAdd: (tenant: string, email: string) => void;
  onRemove: (instance: AccessInstance, person: AccessPerson) => void;
}) {
  if (data.instances.length === 0) return <EmptyState title="Nenhuma instância" description="Crie uma empresa em Empresas." />;
  return (
    <section className="space-y-3" aria-label="Instâncias e administradores">
      {data.instances.map((i) => {
        const suspended = i.tenant_status !== 'active' || i.contract_status !== 'active';
        return (
          <article key={i.tenant_id} aria-label={`Instância ${i.name}`} className="rounded-sheet border border-border-subtle bg-surface p-4">
            <div className="flex flex-wrap items-center gap-3">
              <h2 className="text-base font-semibold text-text-primary">{i.name}</h2>
              <StatusBadge status={suspended ? 'danger' : 'success'}>{suspended ? 'Suspensa' : 'Ativa'}</StatusBadge>
              <span className="text-sm text-text-secondary">{i.direct_agents} na equipe · {i.hub_agents} agentes do Hub</span>
            </div>
            <h3 className="mt-3 text-sm font-medium text-text-primary">Administradores</h3>
            {i.admins.length === 0 ? (
              <p className="mt-1 text-sm text-status-warning">Sem administrador. Adicione alguém abaixo.</p>
            ) : (
              <ul className="mt-1 divide-y divide-border-subtle">
                {i.admins.map((p) => (
                  <li key={p.user_id} className="flex flex-wrap items-center gap-2 py-1.5 text-sm">
                    <span className="font-medium text-text-primary">{p.name || p.email}</span>
                    {p.name && <span className="text-text-secondary">{p.email}</span>}
                    <button type="button" aria-label={`Retirar administração de ${p.email || p.name} em ${i.name}`} className="ml-auto text-xs text-status-danger underline-offset-2 hover:underline" onClick={() => onRemove(i, p)}>Retirar</button>
                  </li>
                ))}
              </ul>
            )}
            {!suspended && (
              <div className="mt-3">
                <AddByEmail label={`Novo administrador de ${i.name}`} placeholder="pessoa@empresa.com.br" busy={adding} onAdd={(email) => onAdd(i.tenant_id, email)}
                  help="Precisa já ter conta no OMNIRA. Para convidar uma pessoa nova, use a Equipe da própria empresa." />
              </div>
            )}
          </article>
        );
      })}
    </section>
  );
}
