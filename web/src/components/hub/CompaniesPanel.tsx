import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { describeHubAdminError, hubAdminAPI, type HubCompany } from '../../lib/hub';
import { handleUnauthorized, isUnauthorized } from '../../lib/session';
import { Button, ConfirmDialog, EmptyState, ErrorState, Input, LoadingState, Modal, StatusBadge } from '../primitives';

// Companies of a Hub (ADR-0038 phase 1), shown as the "Instâncias" tab of the Access panel (ADR-0039): create companies, switch
// them on and off, issue or withdraw their capabilities. Everything shown and done here is decided by the server for the
// signed-in operator; this panel never chooses a tenant by itself, and the caller only mounts it when the server says the
// person may manage companies. `extra` lets the Access panel put each company's administrators inside the same card.
export default function CompaniesPanel({ hubId, extra }: { hubId: string; extra?: (tenantId: string) => ReactNode }) {
  const qc = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [confirm, setConfirm] = useState<HubCompany | null>(null);
  const [notice, setNotice] = useState<{ tone: 'ok' | 'error'; text: string } | null>(null);

  const list = useQuery({ queryKey: ['hub-companies', hubId], enabled: !!hubId, queryFn: () => hubAdminAPI.companies(hubId), retry: false });
  useEffect(() => {
    if (isUnauthorized(list.error)) handleUnauthorized();
  }, [list.error]);

  const refresh = () => qc.invalidateQueries({ queryKey: ['hub-companies', hubId] });
  const update = useMutation({
    mutationFn: (v: { id: string; body: { status?: 'active' | 'suspended'; capabilities?: Record<string, boolean> } }) => hubAdminAPI.update(hubId, v.id, v.body),
    onSuccess: () => { setNotice(null); void refresh(); },
    onError: (err) => { setNotice({ tone: 'error', text: describeHubAdminError(err) }); void refresh(); },
    onSettled: () => setConfirm(null),
  });

  const caps = list.data?.capabilities ?? [];
  const items = list.data?.items ?? [];

  return (
    <section className="space-y-4" aria-label="Instâncias">
      <div className="flex flex-wrap items-start gap-3">
        <p className="max-w-3xl text-sm text-text-secondary">
          Canais de WhatsApp e integrações ERP/CRM são configurados hoje dentro de cada instância, no menu Canais. Configurá-los por aqui é a próxima etapa (ADR-0038, fase 3); os números abaixo só mostram o que já existe.
        </p>
        <div className="ml-auto"><Button onClick={() => setCreating(true)}>Nova instância</Button></div>
      </div>
      {notice && (
        <div role="alert" className={notice.tone === 'error' ? 'rounded-control border border-status-danger-border bg-status-danger-soft px-3 py-2 text-sm text-status-danger' : 'rounded-control border border-status-success-border bg-status-success-soft px-3 py-2 text-sm text-status-success'}>
          {notice.text}
        </div>
      )}
      {list.isLoading && <LoadingState message="Carregando instâncias…" />}
      {list.isError && !isUnauthorized(list.error) && <ErrorState message={describeHubAdminError(list.error)} action={{ label: 'Tentar novamente', onClick: () => void list.refetch() }} />}
      {list.data && items.length === 0 && <EmptyState title="Nenhuma instância" description="Crie a primeira instância deste Hub." />}
      {items.map((c) => (
        <CompanyCard key={c.id} company={c} caps={caps} busy={update.isPending} extra={extra?.(c.id)}
          onToggleCap={(key, on) => update.mutate({ id: c.id, body: { capabilities: { [key]: on } } })}
          onStatus={() => (c.status === 'active' ? setConfirm(c) : update.mutate({ id: c.id, body: { status: 'active' } }))} />
      ))}
      <NewCompanyModal open={creating} hubId={hubId} onClose={() => setCreating(false)}
        onCreated={(c) => { setCreating(false); setNotice({ tone: 'ok', text: `Instância “${c.display_name}” criada. Ninguém tem acesso a ela ainda: conceda o acesso aos atendentes.` }); void refresh(); }} />
      <ConfirmDialog open={!!confirm} title={`Suspender ${confirm?.display_name ?? ''}?`} destructive isPending={update.isPending} confirmLabel="Suspender instância"
        message="A instância deixa de ser atendida: os membros dela e os atendentes do Hub perdem o acesso na hora. Nada é apagado e ela pode ser reativada."
        onCancel={() => setConfirm(null)} onConfirm={() => confirm && update.mutate({ id: confirm.id, body: { status: 'suspended' } })} />
    </section>
  );
}

function CompanyCard({ company: c, caps, busy, extra, onToggleCap, onStatus }: {
  company: HubCompany;
  caps: { key: string; label: string; description: string; gates: string }[];
  busy: boolean;
  extra?: ReactNode;
  onToggleCap: (key: string, on: boolean) => void;
  onStatus: () => void;
}) {
  const suspended = c.status !== 'active';
  return (
    <section aria-label={`Instância ${c.display_name}`} className="rounded-sheet border border-border-subtle bg-surface p-4">
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="text-base font-semibold text-text-primary">{c.display_name}</h2>
        <StatusBadge status={suspended ? 'danger' : 'success'}>{suspended ? 'Suspensa' : 'Ativa'}</StatusBadge>
        {c.contract_status !== 'active' && <StatusBadge status="warning">Contrato {c.contract_status}</StatusBadge>}
        <span className="text-sm text-text-secondary">{c.legal_name !== c.display_name ? c.legal_name : ''}</span>
        <div className="ml-auto">
          <Button size="sm" variant={suspended ? 'primary' : 'secondary'} disabled={busy || (c.status !== 'active' && c.status !== 'suspended')} onClick={onStatus}>
            {suspended ? 'Reativar' : 'Suspender'}
          </Button>
        </div>
      </div>
      <dl className="mt-3 grid grid-cols-2 gap-3 text-sm sm:grid-cols-4">
        <Fact label="Canais WhatsApp" value={c.channels} />
        <Fact label="Integrações ERP/CRM" value={c.integrations} />
        <Fact label="Conversas abertas" value={c.open_conversations} />
        <Fact label="Atendentes com acesso" value={c.agents} />
      </dl>
      <fieldset className="mt-4" disabled={busy}>
        <legend className="text-sm font-medium text-text-primary">Capacidades</legend>
        <div className="mt-2 grid gap-2 sm:grid-cols-2">
          {caps.map((cap) => (
            <label key={cap.key} className="flex items-start gap-2 rounded-control border border-border-subtle p-2 text-sm">
              <input type="checkbox" className="mt-1" checked={c.capabilities[cap.key] !== false} onChange={(e) => onToggleCap(cap.key, e.target.checked)} aria-label={`${cap.label} — ${c.display_name}`} />
              <span>
                <span className="font-medium text-text-primary">{cap.label}</span>
                <span className="block text-text-secondary">{cap.description}</span>
                <span className="block text-xs text-text-tertiary">Ao desligar: {cap.gates}</span>
              </span>
            </label>
          ))}
        </div>
      </fieldset>
      {extra}
    </section>
  );
}

function Fact({ label, value }: { label: string; value: number }) {
  return (
    <div>
      <dt className="text-text-secondary">{label}</dt>
      <dd className="text-lg font-semibold text-text-primary">{value}</dd>
    </div>
  );
}

function NewCompanyModal({ open, hubId, onClose, onCreated }: { open: boolean; hubId: string; onClose: () => void; onCreated: (c: HubCompany) => void }) {
  const [legal, setLegal] = useState('');
  const [trade, setTrade] = useState('');
  const [taxId, setTaxId] = useState('');
  const [admin, setAdmin] = useState('');
  const [error, setError] = useState<string | null>(null);
  // One key per attempt at the SAME content: a double click or a retry cannot create two companies; changing a field starts a new attempt.
  const key = useRef<{ sig: string; id: string } | null>(null);
  const sig = useMemo(() => [legal, trade, taxId, admin].join('\u0000'), [legal, trade, taxId, admin]);

  useEffect(() => {
    if (!open) { setLegal(''); setTrade(''); setTaxId(''); setAdmin(''); setError(null); key.current = null; }
  }, [open]);

  const create = useMutation({
    mutationFn: () => {
      if (!key.current || key.current.sig !== sig) key.current = { sig, id: `new-company-${crypto.randomUUID()}` };
      return hubAdminAPI.create(hubId, { legal_name: legal.trim(), trade_name: trade.trim() || undefined, tax_id: taxId.trim() || undefined, initial_admin_email: admin.trim() || undefined }, key.current.id);
    },
    onSuccess: (c) => onCreated(c),
    onError: (err) => setError(describeHubAdminError(err)),
  });
  const valid = legal.trim().length >= 2;

  return (
    <Modal open={open} title="Nova instância" description="Cria a instância dentro deste Hub. Nenhum atendente recebe acesso automaticamente." onClose={onClose}
      footer={<>
        <Button variant="secondary" size="sm" onClick={onClose} disabled={create.isPending}>Cancelar</Button>
        <Button size="sm" disabled={!valid} isLoading={create.isPending} onClick={() => { setError(null); create.mutate(); }}>Criar instância</Button>
      </>}>
      <form className="space-y-3" onSubmit={(e) => { e.preventDefault(); if (valid && !create.isPending) { setError(null); create.mutate(); } }}>
        <Input label="Razão social" value={legal} onChange={(e) => setLegal(e.target.value)} maxLength={200} required />
        <Input label="Nome fantasia (opcional)" value={trade} onChange={(e) => setTrade(e.target.value)} maxLength={200} />
        <Input label="CNPJ (opcional)" value={taxId} onChange={(e) => setTaxId(e.target.value)} maxLength={32} />
        <Input label="E-mail do primeiro administrador (opcional)" type="email" value={admin} onChange={(e) => setAdmin(e.target.value)} maxLength={254}
          helperText="Precisa ser alguém que já tem conta no OMNIRA. Para convidar uma pessoa nova, use a equipe da própria instância depois." />
        {error && <p role="alert" className="text-sm text-status-danger">{error}</p>}
      </form>
    </Modal>
  );
}
