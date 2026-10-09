import { useEffect, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  describeHubAccessError, hubAccessAPI,
  type AccessAgent, type AccessInstance, type AccessInvitation, type AccessMode, type AccessOverview, type AccessPerson,
} from '../lib/hub';
import { handleUnauthorized, isUnauthorized } from '../lib/session';
import { useMyHubs } from '../hooks/useMyHubs';
import { Button, ConfirmDialog, EmptyState, ErrorState, Input, LoadingState, StatusBadge } from '../components/primitives';
import CompaniesPanel from '../components/hub/CompaniesPanel';
import PoolsPanel from '../components/hub/PoolsPanel';

// Management panel of the Hub (ADR-0038/0039): the companies, who administers each one, which agents exist and what each one may do where.
// One screen with two tabs (Agentes e permissões / Instâncias); the old /hub/instâncias address redirects here.
// Separate from the conversations workspace on purpose. Everything shown and done is decided by the server for the
// signed-in hub admin; `can_manage_access` only decides whether the screen is offered. Only an admin of the HUB can
// authorize an agent for more than one instance: a company's own administrator manages their company's people in
// "Equipe" and has no route into this panel.
type Tab = 'companies' | 'agents' | 'pools';

const MODE_LABEL: Record<AccessMode, string> = { none: 'Sem acesso', read: 'Só leitura', reply: 'Ler e responder' };

// One word on screen (owner decision, 2026-10-09): "instância" is what the user sees for what the code calls a tenant or company.
export function instanceLabel(n: number): string {
  if (n <= 0) return 'Nenhuma instância';
  return n === 1 ? 'Uma instância' : `${n} instâncias`;
}

export default function HubAccessPage() {
  const hubs = useMyHubs();
  const adminOf = (hubs.data ?? []).filter((h) => h.can_manage_access);
  const [picked, setPicked] = useState('');
  const hub = adminOf.find((h) => h.id === picked) ?? adminOf[0];
  const hubId = hub?.id ?? '';
  const qc = useQueryClient();
  const [params] = useSearchParams();
  const [tab, setTab] = useState<Tab>(params.get('aba') === 'instancias' ? 'companies' : params.get('aba') === 'equipes' ? 'pools' : 'agents');
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

  const setManage = useMutation({
    mutationFn: (v: { user: string; tenant: string; can: boolean }) => hubAccessAPI.setManage(hubId, v.user, v.tenant, v.can),
    onSuccess: () => { setNotice(null); void refresh(); },
    onError: fail,
  });
  const setAccess = useMutation({
    mutationFn: (v: { user: string; tenant: string; mode: AccessMode }) => hubAccessAPI.setAccess(hubId, v.user, v.tenant, v.mode),
    onSuccess: () => { setNotice(null); void refresh(); },
    onError: fail,
  });
  const invite = useMutation({
    mutationFn: (v: { email: string; access: { tenant_id: string; mode: 'read' | 'reply' }[] }) => hubAccessAPI.invite(hubId, v.email, v.access),
    onSuccess: (r, v) => ok(r.status === 'applied'
      ? `${v.email} já tinha conta: agora é agente deste Hub${v.access.length ? ' com o acesso escolhido' : '. Defina o acesso na tabela'}.`
      : `${v.email} ainda não tem conta. A autorização fica guardada por 14 dias e vale no primeiro acesso dessa pessoa. Avise-a para entrar no OMNIRA com este e-mail.`),
    onError: fail,
  });
  const revokeInvite = useMutation({
    mutationFn: (id: string) => hubAccessAPI.revokeInvitation(hubId, id),
    onSuccess: () => ok('Autorização cancelada.'),
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
    onSuccess: () => ok('Administração retirada. A pessoa continua na instância como atendente.'),
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
        <Link to="/inbox" className="text-sm text-text-secondary underline-offset-2 hover:underline">← Conversas</Link>
        <h1 className="text-lg font-semibold text-text-primary">Acessos</h1>
        {adminOf.length > 1 ? (
          <select aria-label="Trocar de Hub" value={hubId} onChange={(e) => setPicked(e.target.value)} className="h-9 rounded-control border border-border-light bg-surface px-2 text-sm font-medium text-text-primary">
            {adminOf.map((h) => <option key={h.id} value={h.id}>{h.name}</option>)}
          </select>
        ) : (
          <span className="text-sm text-text-secondary">{hub.name}</span>
        )}
      </div>
      <div className="mx-auto w-full max-w-6xl space-y-4 p-4">
        <div role="tablist" aria-label="Seções do painel de acessos" className="inline-flex rounded-control bg-surface-muted p-1">
          {([['agents', 'Agentes e permissões'], ['companies', 'Instâncias'], ['pools', 'Equipes']] as const).map(([id, label]) => (
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
          <AgentsTab data={data} busy={setAccess.isPending || setManage.isPending} adding={invite.isPending}
            onSet={(user, tenant, mode) => setAccess.mutate({ user, tenant, mode })}
            onManage={(user, tenant, can) => setManage.mutate({ user, tenant, can })}
            onInvite={(email, access) => invite.mutate({ email, access })}
            onCancelInvite={(id) => revokeInvite.mutate(id)}
            onRemove={(a) => setRemoveAgent(a)} />
        )}
        {data && tab === 'pools' && <PoolsPanel hubId={hubId} agents={data.agents} instances={data.instances} />}
        {tab === 'companies' && hub.can_manage_companies && (
          <CompaniesPanel hubId={hubId} extra={(tenantId) => {
            const inst = data?.instances.find((i) => i.tenant_id === tenantId);
            return inst ? <AdminsSection instance={inst} adding={addAdmin.isPending} onAdd={(tenant, email) => addAdmin.mutate({ tenant, email })} onRemove={(i, p) => setRemoveAdmin({ instance: i, person: p })} /> : null;
          }} />
        )}
        {data && tab === 'companies' && !hub.can_manage_companies && (
          <CompaniesAdminsOnly data={data} adding={addAdmin.isPending}
            onAdd={(tenant, email) => addAdmin.mutate({ tenant, email })}
            onRemove={(instance, person) => setRemoveAdmin({ instance, person })} />
        )}
      </div>
      <ConfirmDialog open={!!removeAgent} title={`Remover ${removeAgent?.email || 'agente'} do Hub?`} destructive isPending={dropAgent.isPending} confirmLabel="Remover agente"
        message="Todos os acessos deste agente às instâncias deste Hub terminam na hora. A conta dele não é apagada."
        onCancel={() => setRemoveAgent(null)} onConfirm={() => removeAgent && dropAgent.mutate(removeAgent.user_id)} />
      <ConfirmDialog open={!!removeAdmin} title={`Retirar a administração de ${removeAdmin?.person.email || 'esta pessoa'}?`} destructive isPending={dropAdmin.isPending} confirmLabel="Retirar administração"
        message={`Ela deixa de administrar ${removeAdmin?.instance.name ?? 'a instância'}, mas continua na instância como atendente. Uma instância precisa de ao menos um administrador.`}
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

// One box for both cases: the e-mail of a person who already has an account becomes an agent right away; the e-mail of a
// person who has none is kept and applied at their first sign-in. The access picked here is optional (default: none), and
// can always be changed in the table.
function InvitePanel({ instances, busy, onInvite }: { instances: AccessInstance[]; busy: boolean; onInvite: (email: string, access: { tenant_id: string; mode: 'read' | 'reply' }[]) => void }) {
  const [email, setEmail] = useState('');
  const [picks, setPicks] = useState<Record<string, AccessMode>>({});
  const valid = /^\S+@\S+$/.test(email.trim());
  const open = instances.filter((i) => i.tenant_status === 'active' && i.contract_status === 'active');
  return (
    <form className="space-y-3 rounded-sheet border border-border-subtle bg-surface p-4" aria-label="Adicionar pessoa"
      onSubmit={(e) => {
        e.preventDefault();
        if (!valid || busy) return;
        const access = open.flatMap((i) => (picks[i.tenant_id] === 'read' || picks[i.tenant_id] === 'reply' ? [{ tenant_id: i.tenant_id, mode: picks[i.tenant_id] as 'read' | 'reply' }] : []));
        onInvite(email.trim(), access);
        setEmail('');
        setPicks({});
      }}>
      <div className="flex flex-wrap items-end gap-2">
        <div className="min-w-[16rem] flex-1">
          <Input label="Adicionar pessoa ao Hub" type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="pessoa@empresa.com.br" maxLength={254}
            helperText="Serve para quem já tem conta e para quem ainda não tem: nesse caso, a autorização vale no primeiro acesso, por 14 dias." />
        </div>
        <Button size="sm" type="submit" disabled={!valid} isLoading={busy}>Adicionar</Button>
      </div>
      {open.length > 0 && (
        <fieldset className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
          <legend className="mb-1 text-sm font-medium text-text-primary">Acesso inicial (opcional)</legend>
          {open.map((i) => (
            <label key={i.tenant_id} className="flex items-center justify-between gap-2 text-sm text-text-secondary">
              <span className="truncate">{i.name}</span>
              <select aria-label={`Acesso inicial em ${i.name}`} value={picks[i.tenant_id] ?? 'none'} onChange={(e) => setPicks((p) => ({ ...p, [i.tenant_id]: e.target.value as AccessMode }))}
                className="h-9 rounded-control border border-border-light bg-surface px-2 text-sm text-text-primary focus-visible:ring-2 focus-visible:ring-accent-primary">
                {(Object.keys(MODE_LABEL) as AccessMode[]).map((m) => <option key={m} value={m}>{MODE_LABEL[m]}</option>)}
              </select>
            </label>
          ))}
        </fieldset>
      )}
    </form>
  );
}

function PendingInvitations({ items, nameOf, onCancel }: { items: AccessInvitation[]; nameOf: Map<string, string>; onCancel: (id: string) => void }) {
  if (items.length === 0) return null;
  return (
    <section aria-label="Autorizações aguardando o primeiro acesso" className="rounded-sheet border border-border-subtle bg-surface p-4">
      <h2 className="text-sm font-semibold text-text-primary">Aguardando o primeiro acesso</h2>
      <ul className="mt-2 divide-y divide-border-subtle">
        {items.map((inv) => (
          <li key={inv.id} className="flex flex-wrap items-center gap-2 py-1.5 text-sm">
            <span className="font-medium text-text-primary">{inv.email}</span>
            <span className="text-text-secondary">
              {inv.access.length === 0 ? 'sem acesso a instâncias ainda' : inv.access.map((a) => `${nameOf.get(a.tenant_id) ?? 'outra instância'}: ${MODE_LABEL[a.mode]}`).join(' · ')}
            </span>
            <span className="text-xs text-text-tertiary">vale até {new Date(inv.expires_at).toLocaleDateString('pt-BR')}</span>
            <button type="button" aria-label={`Cancelar a autorização de ${inv.email}`} className="ml-auto text-xs text-status-danger underline-offset-2 hover:underline" onClick={() => onCancel(inv.id)}>Cancelar</button>
          </li>
        ))}
      </ul>
    </section>
  );
}

function AgentsTab({ data, busy, adding, onSet, onManage, onInvite, onCancelInvite, onRemove }: {
  data: AccessOverview; busy: boolean; adding: boolean;
  onSet: (user: string, tenant: string, mode: AccessMode) => void;
  onManage: (user: string, tenant: string, can: boolean) => void;
  onInvite: (email: string, access: { tenant_id: string; mode: 'read' | 'reply' }[]) => void;
  onCancelInvite: (id: string) => void;
  onRemove: (a: AccessAgent) => void;
}) {
  const instances = data.instances;
  const nameOf = new Map(instances.map((i) => [i.tenant_id, i.name]));
  return (
    <section className="space-y-4" aria-label="Agentes e permissões">
      <InvitePanel instances={instances} busy={adding} onInvite={onInvite} />
      <PendingInvitations items={data.invitations ?? []} nameOf={nameOf} onCancel={onCancelInvite} />
      {data.agents.length === 0 && <EmptyState title="Nenhum agente neste Hub" description="Adicione um agente e depois defina em quais instâncias ele atua." />}
      {data.agents.length > 0 && instances.length === 0 && <EmptyState title="Nenhuma instância" description="Crie uma instância na aba Instâncias para poder liberar agentes." />}
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
                const manages = new Map(a.grants.map((g) => [g.tenant_id, !!g.can_manage]));
                const direct = a.direct_instances.map((t) => nameOf.get(t) ?? 'outra instância');
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
                          {value !== 'none' && (i.management_scopes ?? []).length > 0 && !locked && (
                            <label className="mt-1 flex items-center gap-1.5 text-xs text-text-secondary">
                              <input type="checkbox" checked={manages.get(i.tenant_id) ?? false} disabled={busy}
                                onChange={(e) => onManage(a.user_id, i.tenant_id, e.target.checked)}
                                aria-label={`Gerenciar ${i.name} — ${a.email || a.name}`} />
                              Gerenciar canais e integrações
                            </label>
                          )}
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
        Só o administrador do Hub libera um agente em mais de uma instância. O administrador de cada instância gerencia as pessoas dela em “Equipe”, dentro do OMNIRA.
      </p>
    </section>
  );
}

// The administrators of ONE company (an admin of the Hub may name more than one; a company's own administrator manages their agents in "Equipe").
function AdminsSection({ instance: i, adding, onAdd, onRemove }: {
  instance: AccessInstance; adding: boolean;
  onAdd: (tenant: string, email: string) => void;
  onRemove: (instance: AccessInstance, person: AccessPerson) => void;
}) {
  const suspended = i.tenant_status !== 'active' || i.contract_status !== 'active';
  return (
    <div className="mt-4 border-t border-border-subtle pt-3" aria-label={`Administradores de ${i.name}`}>
      <h3 className="text-sm font-medium text-text-primary">Administradores</h3>
      <p className="text-xs text-text-tertiary">{i.direct_agents} na equipe · {i.hub_agents} agentes do Hub</p>
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
            help="Precisa já ter conta no OMNIRA. Para convidar uma pessoa nova, use a Equipe da própria instância." />
        </div>
      )}
    </div>
  );
}

// For an admin of the Hub who may not create or suspend companies: the same companies, with their administrators only.
function CompaniesAdminsOnly({ data, adding, onAdd, onRemove }: {
  data: AccessOverview; adding: boolean;
  onAdd: (tenant: string, email: string) => void;
  onRemove: (instance: AccessInstance, person: AccessPerson) => void;
}) {
  if (data.instances.length === 0) return <EmptyState title="Nenhuma instância" description="Ainda não há instâncias neste Hub." />;
  return (
    <section className="space-y-3" aria-label="Instâncias">
      {data.instances.map((i) => {
        const suspended = i.tenant_status !== 'active' || i.contract_status !== 'active';
        return (
          <article key={i.tenant_id} aria-label={`Instância ${i.name}`} className="rounded-sheet border border-border-subtle bg-surface p-4">
            <div className="flex flex-wrap items-center gap-3">
              <h2 className="text-base font-semibold text-text-primary">{i.name}</h2>
              <StatusBadge status={suspended ? 'danger' : 'success'}>{suspended ? 'Suspensa' : 'Ativa'}</StatusBadge>
            </div>
            <AdminsSection instance={i} adding={adding} onAdd={onAdd} onRemove={onRemove} />
          </article>
        );
      })}
    </section>
  );
}
