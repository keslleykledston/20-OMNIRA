import { useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  GROUP_LINK_ROLE_LABEL,
  describeGroupOrganizerError,
  groupOrganizerAPI,
  hubManageAPI,
  type GroupCatalogEntry,
  type GroupLink,
  type GroupLinkRole,
  type GroupRelationship,
  type GroupRelationshipContext,
  type HubSummary,
} from '../lib/hub';
import { GROUP_PURPOSE_BADGE_VARIANT, GROUP_PURPOSE_LABEL, GROUP_PURPOSE_OPTIONS } from '../lib/groupModel';
import type { GroupPurpose } from '../lib/groups';
import { handleUnauthorized, isUnauthorized } from '../lib/session';
import { Badge, Button, ConfirmDialog, DropdownMenu, EmptyState, ErrorState, LoadingState, Modal, PageHeader, StatusBadge, Tabs, TenantBadge, type MenuAction } from '../components/primitives';
import { useMyHubs } from '../hooks/useMyHubs';

// Organizador de grupos (ADR-0059, Gate 3): pastas virtuais por Tenant, Kanban e lista para os metadados de grupo já
// catalogados pelo Hub (Gate 2). Isto NUNCA lê mensagens, nunca gerencia membros e nunca envia nada a um grupo —
// é organização de METADADOS. Vincular/transferir/revogar e a pasta compartilhada Alpha↔Beta são sempre o que o
// servidor devolve (RLS por linha); esta tela não decide isso, só reflete.
//
// Decisão D-12 (aprovada): sem arrastar-e-soltar nesta fatia. Mover um grupo entre pastas é uma ação explícita de
// menu/teclado, que chama a MESMA rota de vínculo/transferência do Gate 2 — nunca um efeito colateral de clique.

type View = 'list' | 'folders' | 'kanban';
type Tenant = { id: string; name: string };

const SOURCE_STATE: Record<GroupCatalogEntry['source_state'], { status: 'success' | 'warning' | 'danger' | 'default'; label: string }> = {
  available: { status: 'success', label: 'Disponível na origem' },
  stale: { status: 'warning', label: 'Desatualizado' },
  unavailable: { status: 'danger', label: 'Indisponível na origem' },
  archived: { status: 'default', label: 'Arquivado' },
};

interface OrganizerData {
  tenants: Tenant[];
  catalog: GroupCatalogEntry[];
  relationships: GroupRelationship[];
  linksByGroup: Record<string, GroupLink[]>;
  classificationByGroup: Record<string, GroupRelationshipContext>;
}

async function loadOrganizerData(hubId: string): Promise<OrganizerData> {
  // One request for every catalog entry's links/classification, not one request PER entry: that N+1
  // pattern hung the page indefinitely once autodiscovery (ADR-0059 addendum) grew a real catalog to
  // dozens of groups.
  const [managed, catalog, relationships, links, classification] = await Promise.all([
    hubManageAPI.managed(hubId),
    groupOrganizerAPI.catalog(hubId),
    groupOrganizerAPI.relationships(hubId),
    groupOrganizerAPI.linksForHub(hubId),
    groupOrganizerAPI.classification(hubId),
  ]);
  const tenants = managed.filter((m) => m.scopes.includes('groups')).map((m) => ({ id: m.tenant_id, name: m.name }));
  const linksByGroup: Record<string, GroupLink[]> = {};
  for (const l of links) (linksByGroup[l.hub_group_id] ??= []).push(l);
  const classificationByGroup: Record<string, GroupRelationshipContext> = {};
  for (const c of classification) classificationByGroup[c.hub_group_id] = c;
  return { tenants, catalog, relationships, linksByGroup, classificationByGroup };
}

function activeLinks(data: OrganizerData, entryId: string): GroupLink[] {
  return (data.linksByGroup[entryId] ?? []).filter((l) => l.status === 'active');
}

interface Folder {
  key: string;
  name: string;
  shared?: { relationship: GroupRelationship };
  entries: GroupCatalogEntry[];
}

// Pastas/Kanban agrupam por CLASSIFICAÇÃO (wa_groups.purpose, lida read-only do tenant — ver
// EntryClassificationBadge) desde que o Tenant deixou de ser um eixo útil de agrupamento (achado do
// dono, 2026-11-12): quando todo grupo de um Hub já pertence ao mesmo único Tenant, uma pasta "por
// Tenant" é sempre a mesma pasta vazia. O vínculo a Tenant (onLink/"Vincular a um Tenant") continua
// existindo por grupo — só deixou de decidir em qual pasta o cartão aparece. 'other' (Outros) é sempre
// uma pasta válida, nunca "sem pasta": todo catalog entry tem uma classificação (purpose default 'other'
// no próprio wa_groups), inclusive quando a entrada ainda não tem linha em wa_groups (ver
// EntryClassificationBadge, "Sem classificação no CRM" — mesmo caso, badge separado).
function buildClassificationFolders(data: OrganizerData): Folder[] {
  const byPurpose = new Map<GroupPurpose, Folder>(
    GROUP_PURPOSE_OPTIONS.map((o) => [o.value, { key: o.value, name: o.label, entries: [] }]),
  );
  for (const entry of data.catalog) {
    const purpose = data.classificationByGroup[entry.id]?.purpose ?? 'other';
    byPurpose.get(purpose)!.entries.push(entry);
  }
  return [...byPurpose.values()];
}

function buildSharedFolders(data: OrganizerData): Folder[] {
  // Pastas compartilhadas (D-07): SEMPRE derivadas de uma relação comercial ativa + os vínculos ativos de AMBOS os
  // lados agora — nunca uma coleção manual. Se a relação for revogada, a pasta desaparece no próximo carregamento.
  return data.relationships
    .filter((r) => r.status === 'active')
    .map((r) => {
      const name = `${data.tenants.find((t) => t.id === r.consumer_tenant_id)?.name ?? r.consumer_tenant_id} ↔ ${data.tenants.find((t) => t.id === r.supplier_tenant_id)?.name ?? r.supplier_tenant_id}`;
      const entries = data.catalog.filter((entry) => {
        const tenantIds = new Set(activeLinks(data, entry.id).map((l) => l.tenant_id));
        return tenantIds.has(r.consumer_tenant_id) && tenantIds.has(r.supplier_tenant_id);
      });
      return { key: r.id, name, shared: { relationship: r }, entries };
    });
}

export default function HubGroupOrganizerPage() {
  const hubs = useMyHubs();
  const mine = (hubs.data ?? []).filter((h) => h.can_manage_groups);
  useEffect(() => {
    if (isUnauthorized(hubs.error)) handleUnauthorized();
  }, [hubs.error]);
  if (hubs.isLoading) return <div className="p-6"><LoadingState message="Carregando…" /></div>;
  if (mine.length === 0) {
    return (
      <div className="p-6">
        <EmptyState
          title="Nenhum Hub para organizar grupos"
          description="O Hub ainda não delegou a você a organização de grupos de nenhuma instância. Isso é um escopo de gestão à parte ('groups'), não vem junto com canais/integrações."
        />
      </div>
    );
  }
  return (
    <div className="space-y-8 p-4">
      {mine.map((h) => <HubGroupOrganizer key={h.id} hub={h} showHeading={mine.length > 1} />)}
    </div>
  );
}

function HubGroupOrganizer({ hub, showHeading }: { hub: HubSummary; showHeading: boolean }) {
  const qc = useQueryClient();
  const queryKey = ['group-organizer', hub.id];
  const data = useQuery({ queryKey, queryFn: () => loadOrganizerData(hub.id), retry: false });
  useEffect(() => {
    if (isUnauthorized(data.error)) handleUnauthorized();
  }, [data.error]);
  const [view, setView] = useState<View>('list');
  const [addOpen, setAddOpen] = useState(false);
  const [relOpen, setRelOpen] = useState(false);
  const [linkFor, setLinkFor] = useState<GroupCatalogEntry | null>(null);
  const [transfer, setTransfer] = useState<{ entry: GroupCatalogEntry; link: GroupLink } | null>(null);
  const [revoke, setRevoke] = useState<{ entry: GroupCatalogEntry; link: GroupLink } | null>(null);
  const [revokeRel, setRevokeRel] = useState<GroupRelationship | null>(null);

  const invalidate = () => qc.invalidateQueries({ queryKey });

  if (data.isLoading) return <LoadingState message="Carregando catálogo…" />;
  if (data.isError && !isUnauthorized(data.error)) {
    return <ErrorState message="Não foi possível carregar o organizador de grupos." action={{ label: 'Tentar novamente', onClick: () => void data.refetch() }} />;
  }
  const d = data.data;
  if (!d) return null;
  const classificationFolders = buildClassificationFolders(d);
  const sharedFolders = buildSharedFolders(d);
  const tenantName = (id: string) => d.tenants.find((t) => t.id === id)?.name ?? id;

  const linkActions = (entry: GroupCatalogEntry, link: GroupLink): MenuAction[] => [
    { label: 'Transferir para outro Tenant', onSelect: () => setTransfer({ entry, link }) },
    { label: 'Revogar este vínculo', destructive: true, onSelect: () => setRevoke({ entry, link }) },
  ];

  return (
    <section className="space-y-4" aria-label={showHeading ? hub.name : undefined}>
      <PageHeader
        title={showHeading ? `Organizador de grupos — ${hub.name}` : 'Organizador de grupos'}
        description="Pastas virtuais por classificação (Cliente, Parceiro, Fornecedor, Interno), relações comerciais compartilhadas e vínculos de grupo WhatsApp. Organização de metadados — nunca mensagens, nunca envio."
        actions={
          <>
            <Button variant="secondary" size="sm" onClick={() => setRelOpen(true)}>Nova relação comercial</Button>
            <Button size="sm" onClick={() => setAddOpen(true)}>Adicionar grupo ao catálogo</Button>
          </>
        }
      />
      <Tabs
        aria-label="Visão do organizador de grupos"
        value={view}
        onChange={setView}
        items={[
          { id: 'list', label: 'Lista', count: d.catalog.length },
          { id: 'folders', label: 'Pastas', count: classificationFolders.filter((f) => f.entries.length > 0).length + sharedFolders.length },
          { id: 'kanban', label: 'Kanban' },
        ]}
      />
      {d.catalog.length === 0 ? (
        <EmptyState title="Nenhum grupo no catálogo ainda" description="Adicione um grupo (dados de origem conhecidos) para começar a organizá-lo." />
      ) : view === 'list' ? (
        <ListView data={d} tenantName={tenantName} onLink={setLinkFor} linkActions={linkActions} />
      ) : view === 'folders' ? (
        <FoldersView classificationFolders={classificationFolders} sharedFolders={sharedFolders} onLink={setLinkFor} linkActions={linkActions} data={d} onRevokeRelationship={setRevokeRel} />
      ) : (
        <KanbanView classificationFolders={classificationFolders} sharedFolders={sharedFolders} onLink={setLinkFor} linkActions={linkActions} data={d} />
      )}

      <AddCatalogEntryModal hubId={hub.id} tenants={d.tenants} open={addOpen} onClose={() => setAddOpen(false)} onDone={() => { setAddOpen(false); invalidate(); }} />
      <CreateRelationshipModal hubId={hub.id} tenants={d.tenants} open={relOpen} onClose={() => setRelOpen(false)} onDone={() => { setRelOpen(false); invalidate(); }} />
      {linkFor && (
        <CreateLinkModal
          hubId={hub.id}
          entry={linkFor}
          tenants={d.tenants.filter((t) => !activeLinks(d, linkFor.id).some((l) => l.tenant_id === t.id))}
          open
          onClose={() => setLinkFor(null)}
          onDone={() => { setLinkFor(null); invalidate(); }}
        />
      )}
      {transfer && (
        <TransferLinkModal
          hubId={hub.id}
          entry={transfer.entry}
          link={transfer.link}
          tenants={d.tenants.filter((t) => !activeLinks(d, transfer.entry.id).some((l) => l.tenant_id === t.id))}
          open
          onClose={() => setTransfer(null)}
          onDone={() => { setTransfer(null); invalidate(); }}
        />
      )}
      {revoke && (
        <RevokeLinkConfirm hubId={hub.id} entry={revoke.entry} link={revoke.link} tenantName={tenantName} open onClose={() => setRevoke(null)} onDone={() => { setRevoke(null); invalidate(); }} />
      )}
      {revokeRel && (
        <RevokeRelationshipConfirm hubId={hub.id} relationship={revokeRel} tenantName={tenantName} open onClose={() => setRevokeRel(null)} onDone={() => { setRevokeRel(null); invalidate(); }} />
      )}
    </section>
  );
}

function EntryLinkBadges({ entry, data, tenantName, linkActions }: { entry: GroupCatalogEntry; data: OrganizerData; tenantName: (id: string) => string; linkActions: (entry: GroupCatalogEntry, link: GroupLink) => MenuAction[] }) {
  const links = activeLinks(data, entry.id);
  if (links.length === 0) return <span className="text-xs text-text-tertiary">Sem classificação</span>;
  return (
    <ul className="flex flex-wrap items-center gap-2">
      {links.map((l) => (
        <li key={l.tenant_id} className="flex items-center gap-1 rounded-control border border-border-subtle px-2 py-1">
          <TenantBadge name={tenantName(l.tenant_id)} />
          <span className="text-xs text-text-secondary">{GROUP_LINK_ROLE_LABEL[l.role]}</span>
          <DropdownMenu label={`Ações do vínculo com ${tenantName(l.tenant_id)}`} actions={linkActions(entry, l)} />
        </li>
      ))}
    </ul>
  );
}

// Read-only: wa_groups.purpose + the linked CRM account's name, set on the tenant's own Groups page
// (ADR-0059 addendum 5). "Sem classificação" covers both "not yet tagged" and "not yet matched" (the
// catalog entry's group was never opened once in the tenant's own Groups page, so wa_groups has no row
// for it yet) — the two are indistinguishable here on purpose, same convention as hub_discover_tenant_groups.
function EntryClassificationBadge({ entry, data }: { entry: GroupCatalogEntry; data: OrganizerData }) {
  const c = data.classificationByGroup[entry.id];
  if (!c) return <span className="text-xs text-text-tertiary">Sem classificação no CRM</span>;
  return (
    <span className="flex flex-wrap items-center gap-1.5">
      <Badge variant={GROUP_PURPOSE_BADGE_VARIANT[c.purpose]} size="sm">{GROUP_PURPOSE_LABEL[c.purpose]}</Badge>
      {c.account_display_name && <span className="truncate text-xs text-text-tertiary">{c.account_display_name}</span>}
    </span>
  );
}

function ListView({ data, tenantName, onLink, linkActions }: {
  data: OrganizerData; tenantName: (id: string) => string; onLink: (e: GroupCatalogEntry) => void; linkActions: (entry: GroupCatalogEntry, link: GroupLink) => MenuAction[];
}) {
  return (
    <table className="w-full border-collapse text-sm">
      <thead>
        <tr className="border-b border-border-subtle text-left text-text-secondary">
          <th className="py-2 pr-3">Grupo</th>
          <th className="py-2 pr-3">Origem</th>
          <th className="py-2 pr-3">Estado</th>
          <th className="py-2 pr-3">Classificação (CRM)</th>
          <th className="py-2 pr-3">Vínculos</th>
          <th className="py-2 pr-3" />
        </tr>
      </thead>
      <tbody>
        {data.catalog.map((entry) => (
          <tr key={entry.id} className="border-b border-border-subtle/60">
            <td className="py-3 pr-3">
              <div className="font-medium text-text-primary">{entry.display_name || '(sem nome)'}</div>
              <div className="text-xs text-text-tertiary">{entry.provider_group_id}</div>
            </td>
            <td className="py-3 pr-3"><TenantBadge name={tenantName(entry.source_tenant_id)} /></td>
            <td className="py-3 pr-3"><StatusBadge status={SOURCE_STATE[entry.source_state].status}>{SOURCE_STATE[entry.source_state].label}</StatusBadge></td>
            <td className="py-3 pr-3"><EntryClassificationBadge entry={entry} data={data} /></td>
            <td className="py-3 pr-3"><EntryLinkBadges entry={entry} data={data} tenantName={tenantName} linkActions={linkActions} /></td>
            <td className="py-3 pr-3 text-right">
              <Button variant="secondary" size="sm" onClick={() => onLink(entry)}>Vincular a um Tenant</Button>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function EntryCard({ entry, data, tenantName, linkActions, onLink }: {
  entry: GroupCatalogEntry; data: OrganizerData; tenantName: (id: string) => string; linkActions: (entry: GroupCatalogEntry, link: GroupLink) => MenuAction[]; onLink: (e: GroupCatalogEntry) => void;
}) {
  return (
    <div className="space-y-2 rounded-card border border-border-subtle bg-surface p-3">
      <div className="flex items-start justify-between gap-2">
        <div>
          <div className="font-medium text-text-primary">{entry.display_name || '(sem nome)'}</div>
          <div className="text-xs text-text-tertiary">{entry.provider_group_id}</div>
        </div>
        <Button variant="tertiary" size="sm" onClick={() => onLink(entry)}>Vincular…</Button>
      </div>
      <EntryClassificationBadge entry={entry} data={data} />
      <EntryLinkBadges entry={entry} data={data} tenantName={tenantName} linkActions={linkActions} />
    </div>
  );
}

function FoldersView({ classificationFolders, sharedFolders, data, onLink, linkActions, onRevokeRelationship }: {
  classificationFolders: Folder[]; sharedFolders: Folder[]; data: OrganizerData;
  onLink: (e: GroupCatalogEntry) => void; linkActions: (entry: GroupCatalogEntry, link: GroupLink) => MenuAction[]; onRevokeRelationship: (r: GroupRelationship) => void;
}) {
  const tenantName = (id: string) => data.tenants.find((t) => t.id === id)?.name ?? id;
  const folders = [
    ...classificationFolders.filter((f) => f.entries.length > 0).map((f) => ({ ...f, isClassification: true })),
    ...sharedFolders.map((f) => ({ ...f, isClassification: false })),
  ];
  return (
    <div className="space-y-6">
      {folders.map((folder) => (
        <section key={folder.key} aria-label={`Pasta ${folder.name}`} className="space-y-2">
          <div className="flex items-center gap-2">
            {folder.isClassification
              ? <Badge variant={GROUP_PURPOSE_BADGE_VARIANT[folder.key as GroupPurpose]}>{folder.name}</Badge>
              : <h2 className="text-sm font-semibold text-text-primary">{folder.name}</h2>}
            <span className="text-xs text-text-tertiary">{folder.entries.length} grupo(s)</span>
            {folder.shared && (
              <Button variant="tertiary" size="sm" className="ml-auto" onClick={() => onRevokeRelationship(folder.shared!.relationship)}>Revogar relação comercial</Button>
            )}
          </div>
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {folder.entries.map((entry) => <EntryCard key={entry.id} entry={entry} data={data} tenantName={tenantName} linkActions={linkActions} onLink={onLink} />)}
          </div>
        </section>
      ))}
    </div>
  );
}

// Kanban SEM arrastar-e-soltar (D-12): colunas são só uma projeção de leitura das mesmas pastas; mover um cartão é
// sempre pelo menu "Vincular…"/"Transferir…" de cada cartão, que chama a mesma rota HTTP da visão de Lista/Pastas.
function KanbanView({ classificationFolders, sharedFolders, data, onLink, linkActions }: {
  classificationFolders: Folder[]; sharedFolders: Folder[]; data: OrganizerData;
  onLink: (e: GroupCatalogEntry) => void; linkActions: (entry: GroupCatalogEntry, link: GroupLink) => MenuAction[];
}) {
  const tenantName = (id: string) => data.tenants.find((t) => t.id === id)?.name ?? id;
  const columns: Folder[] = [...classificationFolders, ...sharedFolders];
  return (
    <div className="flex gap-4 overflow-x-auto pb-4" role="list" aria-label="Quadro de grupos por pasta">
      {columns.map((col) => (
        <div key={col.key} role="listitem" aria-label={`Coluna ${col.name}`} className="w-72 flex-shrink-0 space-y-2 rounded-card border border-border-subtle bg-surface-muted p-3">
          <div className="flex items-center justify-between">
            <h2 className="text-sm font-semibold text-text-primary">{col.name}</h2>
            <span className="text-xs text-text-tertiary">{col.entries.length}</span>
          </div>
          <div className="space-y-2">
            {col.entries.map((entry) => <EntryCard key={entry.id} entry={entry} data={data} tenantName={tenantName} linkActions={linkActions} onLink={onLink} />)}
            {col.entries.length === 0 && <p className="text-xs text-text-tertiary">Nenhum grupo nesta pasta.</p>}
          </div>
        </div>
      ))}
    </div>
  );
}

function TenantSelect({ id, label, tenants, value, onChange, exclude }: { id: string; label: string; tenants: Tenant[]; value: string; onChange: (v: string) => void; exclude?: string }) {
  const options = tenants.filter((t) => t.id !== exclude);
  return (
    <label className="block space-y-1 text-sm" htmlFor={id}>
      <span className="font-medium text-text-primary">{label}</span>
      <select id={id} value={value} onChange={(e) => onChange(e.target.value)} className="h-9 w-full rounded-control border border-border-light bg-surface px-2 text-sm text-text-primary focus-visible:ring-2 focus-visible:ring-accent-primary">
        <option value="">Escolha um Tenant…</option>
        {options.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}
      </select>
    </label>
  );
}

function RoleSelect({ id, value, onChange }: { id: string; value: GroupLinkRole; onChange: (v: GroupLinkRole) => void }) {
  return (
    <label className="block space-y-1 text-sm" htmlFor={id}>
      <span className="font-medium text-text-primary">Papel do Tenant neste grupo</span>
      <select id={id} value={value} onChange={(e) => onChange(e.target.value as GroupLinkRole)} className="h-9 w-full rounded-control border border-border-light bg-surface px-2 text-sm text-text-primary focus-visible:ring-2 focus-visible:ring-accent-primary">
        {(Object.keys(GROUP_LINK_ROLE_LABEL) as GroupLinkRole[]).map((r) => <option key={r} value={r}>{GROUP_LINK_ROLE_LABEL[r]}</option>)}
      </select>
    </label>
  );
}

const uuidRe = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

function AddCatalogEntryModal({ hubId, tenants, open, onClose, onDone }: { hubId: string; tenants: Tenant[]; open: boolean; onClose: () => void; onDone: () => void }) {
  const [tenantId, setTenantId] = useState('');
  const [connectionId, setConnectionId] = useState('');
  const [connManual, setConnManual] = useState(false);
  const [providerGroupId, setProviderGroupId] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [manual, setManual] = useState(false);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    if (open) { setTenantId(''); setConnectionId(''); setConnManual(false); setProviderGroupId(''); setDisplayName(''); setManual(false); setError(null); }
  }, [open]);
  // Real WhatsApp connections already configured for the chosen tenant, so the operator picks one instead
  // of typing a connection UUID by hand with nowhere in the product to copy it from (found broken live in
  // production, same gap as the group picker below — fixed one step earlier).
  const canDiscoverConnections = open && !!tenantId;
  const connections = useQuery({
    queryKey: ['hub-connection-discover', hubId, tenantId],
    queryFn: () => groupOrganizerAPI.discoverConnections(hubId, tenantId),
    enabled: canDiscoverConnections,
    retry: false,
  });
  useEffect(() => { setConnectionId(''); setConnManual(false); }, [tenantId]);
  const connDiscovered = connections.data ?? [];
  const showConnectionPicker = canDiscoverConnections && !connManual && !connections.isError;
  const canDiscover = open && !!tenantId && uuidRe.test(connectionId);
  // Real groups already known to the tenant's own wa_groups, so the operator picks one instead of typing
  // a JID by hand with nowhere in the product to copy it from (found broken live in production). A group
  // the tenant's own team has never opened once in their Groups page will not be offered here yet — manual
  // entry (below) always stays available as a fallback.
  const discovery = useQuery({
    queryKey: ['hub-group-discover', hubId, tenantId, connectionId],
    queryFn: () => groupOrganizerAPI.discover(hubId, tenantId, connectionId),
    enabled: canDiscover,
    retry: false,
  });
  useEffect(() => { setProviderGroupId(''); setDisplayName(''); setManual(false); }, [tenantId, connectionId]);
  const mutation = useMutation({
    mutationFn: () => groupOrganizerAPI.upsertCatalogEntry(hubId, { source_tenant_id: tenantId, source_channel_connection_id: connectionId, provider_group_id: providerGroupId, display_name: displayName }),
    onSuccess: onDone,
    onError: (err) => { if (isUnauthorized(err)) handleUnauthorized(); else setError(describeGroupOrganizerError(err)); },
  });
  const valid = tenantId && connectionId && providerGroupId;
  const discovered = discovery.data ?? [];
  const showPicker = canDiscover && !manual && !discovery.isError;
  return (
    <Modal open={open} title="Adicionar grupo ao catálogo" description="Metadados mínimos do grupo (nunca mensagens). Conexão e identificador de grupo são os já usados na origem." onClose={onClose}
      footer={<>
        <Button variant="secondary" size="sm" onClick={onClose} disabled={mutation.isPending}>Cancelar</Button>
        <Button size="sm" onClick={() => mutation.mutate()} disabled={!valid || mutation.isPending} isLoading={mutation.isPending}>Adicionar</Button>
      </>}>
      <div className="space-y-3">
        <TenantSelect id="catalog-tenant" label="Tenant de origem" tenants={tenants} value={tenantId} onChange={setTenantId} />
        {showConnectionPicker ? (
          <label className="block space-y-1 text-sm" htmlFor="catalog-connection-picker">
            <span className="font-medium text-text-primary">Conexão (canal) na origem</span>
            {connections.isLoading ? (
              <p className="text-sm text-text-secondary">Buscando conexões de WhatsApp desse Tenant…</p>
            ) : connDiscovered.length === 0 ? (
              <p className="text-sm text-text-secondary">
                Nenhuma conexão de WhatsApp disponível para esse Tenant ainda.{' '}
                <button type="button" className="text-accent-primary underline" onClick={() => setConnManual(true)}>Digitar manualmente</button>
              </p>
            ) : (
              <>
                <select
                  id="catalog-connection-picker"
                  value={connectionId}
                  onChange={(e) => setConnectionId(e.target.value)}
                  className="h-9 w-full rounded-control border border-border-light bg-surface px-2 text-sm text-text-primary focus-visible:ring-2 focus-visible:ring-accent-primary"
                >
                  <option value="">Selecione uma conexão</option>
                  {connDiscovered.map((c) => (
                    <option key={c.id} value={c.id}>
                      WhatsApp · {c.external_number_id} {c.status !== 'active' ? `(${c.status})` : ''}
                    </option>
                  ))}
                </select>
                <button type="button" className="text-xs text-accent-primary underline" onClick={() => setConnManual(true)}>Não encontrou? Digitar manualmente</button>
              </>
            )}
          </label>
        ) : (
          <label className="block space-y-1 text-sm" htmlFor="catalog-connection">
            <span className="font-medium text-text-primary">ID da conexão (canal) na origem</span>
            <input id="catalog-connection" value={connectionId} onChange={(e) => setConnectionId(e.target.value)} placeholder="UUID da conexão" className="h-9 w-full rounded-control border border-border-light bg-surface px-2 text-sm" />
            {canDiscoverConnections && <button type="button" className="block text-xs text-accent-primary underline" onClick={() => setConnManual(false)}>Voltar para a lista de conexões</button>}
          </label>
        )}
        {showPicker ? (
          <label className="block space-y-1 text-sm" htmlFor="catalog-group-picker">
            <span className="font-medium text-text-primary">Grupo</span>
            {discovery.isLoading ? (
              <p className="text-sm text-text-secondary">Buscando grupos já conhecidos nesta conexão…</p>
            ) : discovered.length === 0 ? (
              <p className="text-sm text-text-secondary">
                Nenhum grupo disponível nessa conexão ainda (precisa ter sido aberto ao menos uma vez na tela de Grupos do Tenant).{' '}
                <button type="button" className="text-accent-primary underline" onClick={() => setManual(true)}>Digitar manualmente</button>
              </p>
            ) : (
              <>
                <select
                  id="catalog-group-picker"
                  value={providerGroupId}
                  onChange={(e) => {
                    const g = discovered.find((d) => d.provider_group_id === e.target.value);
                    setProviderGroupId(e.target.value);
                    setDisplayName(g?.name ?? '');
                  }}
                  className="h-9 w-full rounded-control border border-border-light bg-surface px-2 text-sm text-text-primary focus-visible:ring-2 focus-visible:ring-accent-primary"
                >
                  <option value="">Selecione um grupo</option>
                  {discovered.map((g) => <option key={g.provider_group_id} value={g.provider_group_id}>{g.name || g.provider_group_id}</option>)}
                </select>
                <button type="button" className="text-xs text-accent-primary underline" onClick={() => setManual(true)}>Não encontrou? Digitar manualmente</button>
              </>
            )}
          </label>
        ) : (
          <>
            <label className="block space-y-1 text-sm" htmlFor="catalog-group-id">
              <span className="font-medium text-text-primary">Identificador do grupo (provider_group_id)</span>
              <input id="catalog-group-id" value={providerGroupId} onChange={(e) => setProviderGroupId(e.target.value)} placeholder="1203xxxxxxxxx@g.us" className="h-9 w-full rounded-control border border-border-light bg-surface px-2 text-sm" />
            </label>
            <label className="block space-y-1 text-sm" htmlFor="catalog-display-name">
              <span className="font-medium text-text-primary">Nome de exibição</span>
              <input id="catalog-display-name" value={displayName} onChange={(e) => setDisplayName(e.target.value)} className="h-9 w-full rounded-control border border-border-light bg-surface px-2 text-sm" />
            </label>
            {canDiscover && <button type="button" className="text-xs text-accent-primary underline" onClick={() => setManual(false)}>Voltar para a lista de grupos</button>}
          </>
        )}
        {error && <p role="alert" className="text-sm text-status-danger">{error}</p>}
      </div>
    </Modal>
  );
}

function CreateLinkModal({ hubId, entry, tenants, open, onClose, onDone }: { hubId: string; entry: GroupCatalogEntry; tenants: Tenant[]; open: boolean; onClose: () => void; onDone: () => void }) {
  const [tenantId, setTenantId] = useState('');
  const [role, setRole] = useState<GroupLinkRole>('consumer');
  const [error, setError] = useState<string | null>(null);
  const [key] = useState(() => crypto.randomUUID());
  useEffect(() => { if (open) { setTenantId(''); setRole('consumer'); setError(null); } }, [open]);
  const mutation = useMutation({
    mutationFn: () => groupOrganizerAPI.createLink(hubId, entry.id, tenantId, role, key),
    onSuccess: onDone,
    onError: (err) => { if (isUnauthorized(err)) handleUnauthorized(); else setError(describeGroupOrganizerError(err)); },
  });
  return (
    <Modal open={open} title="Vincular a um Tenant" description={`"${entry.display_name || entry.provider_group_id}" passa a aparecer também na pasta do Tenant escolhido, sem remover os vínculos que já existem.`} onClose={onClose}
      footer={<>
        <Button variant="secondary" size="sm" onClick={onClose} disabled={mutation.isPending}>Cancelar</Button>
        <Button size="sm" onClick={() => mutation.mutate()} disabled={!tenantId || mutation.isPending} isLoading={mutation.isPending}>Vincular</Button>
      </>}>
      <div className="space-y-3">
        <TenantSelect id="link-tenant" label="Tenant" tenants={tenants} value={tenantId} onChange={setTenantId} />
        <RoleSelect id="link-role" value={role} onChange={setRole} />
        {error && <p role="alert" className="text-sm text-status-danger">{error}</p>}
      </div>
    </Modal>
  );
}

function TransferLinkModal({ hubId, entry, link, tenants, open, onClose, onDone }: { hubId: string; entry: GroupCatalogEntry; link: GroupLink; tenants: Tenant[]; open: boolean; onClose: () => void; onDone: () => void }) {
  const [tenantId, setTenantId] = useState('');
  const [role, setRole] = useState<GroupLinkRole>(link.role);
  const [error, setError] = useState<string | null>(null);
  const [key] = useState(() => crypto.randomUUID());
  useEffect(() => { if (open) { setTenantId(''); setRole(link.role); setError(null); } }, [open, link.role]);
  const mutation = useMutation({
    mutationFn: () => groupOrganizerAPI.transferLink(hubId, entry.id, link.tenant_id, tenantId, role, link.version, key),
    onSuccess: onDone,
    onError: (err) => { if (isUnauthorized(err)) handleUnauthorized(); else setError(describeGroupOrganizerError(err)); },
  });
  return (
    <Modal open={open} title="Transferir vínculo" description="O vínculo ATUAL é removido e um novo é criado no Tenant de destino, na mesma operação." onClose={onClose}
      footer={<>
        <Button variant="secondary" size="sm" onClick={onClose} disabled={mutation.isPending}>Cancelar</Button>
        <Button size="sm" onClick={() => mutation.mutate()} disabled={!tenantId || mutation.isPending} isLoading={mutation.isPending}>Transferir</Button>
      </>}>
      <div className="space-y-3">
        <TenantSelect id="transfer-tenant" label="Transferir para" tenants={tenants} value={tenantId} onChange={setTenantId} />
        <RoleSelect id="transfer-role" value={role} onChange={setRole} />
        {error && <p role="alert" className="text-sm text-status-danger">{error}</p>}
      </div>
    </Modal>
  );
}

function RevokeLinkConfirm({ hubId, entry, link, tenantName, open, onClose, onDone }: { hubId: string; entry: GroupCatalogEntry; link: GroupLink; tenantName: (id: string) => string; open: boolean; onClose: () => void; onDone: () => void }) {
  const mutation = useMutation({
    mutationFn: () => groupOrganizerAPI.revokeLink(hubId, entry.id, link.tenant_id, link.version),
    onSuccess: onDone,
    onError: (err) => { if (isUnauthorized(err)) handleUnauthorized(); },
  });
  return (
    <ConfirmDialog
      open={open}
      title="Revogar vínculo"
      message={`"${entry.display_name || entry.provider_group_id}" deixa de aparecer na pasta de ${tenantName(link.tenant_id)}. Isso não afeta o grupo no WhatsApp nem suas mensagens.`}
      confirmLabel="Revogar"
      destructive
      isPending={mutation.isPending}
      onConfirm={() => mutation.mutate()}
      onCancel={onClose}
    />
  );
}

function CreateRelationshipModal({ hubId, tenants, open, onClose, onDone }: { hubId: string; tenants: Tenant[]; open: boolean; onClose: () => void; onDone: () => void }) {
  const [consumer, setConsumer] = useState('');
  const [supplier, setSupplier] = useState('');
  const [serviceKind, setServiceKind] = useState('');
  const [serviceRef, setServiceRef] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [key] = useState(() => crypto.randomUUID());
  useEffect(() => { if (open) { setConsumer(''); setSupplier(''); setServiceKind(''); setServiceRef(''); setError(null); } }, [open]);
  const mutation = useMutation({
    mutationFn: () => groupOrganizerAPI.createRelationship(hubId, consumer, supplier, serviceKind, serviceRef, key),
    onSuccess: onDone,
    onError: (err) => { if (isUnauthorized(err)) handleUnauthorized(); else setError(describeGroupOrganizerError(err)); },
  });
  const valid = consumer && supplier && consumer !== supplier && serviceKind;
  return (
    <Modal open={open} title="Nova relação comercial" description="Ex.: Tenant Alpha compra um serviço do Tenant Beta. Isto é só metadado de relação — nunca soma permissões dos dois Tenants numa operação." onClose={onClose}
      footer={<>
        <Button variant="secondary" size="sm" onClick={onClose} disabled={mutation.isPending}>Cancelar</Button>
        <Button size="sm" onClick={() => mutation.mutate()} disabled={!valid || mutation.isPending} isLoading={mutation.isPending}>Criar relação</Button>
      </>}>
      <div className="space-y-3">
        <TenantSelect id="rel-consumer" label="Tenant consumidor (compra o serviço)" tenants={tenants} value={consumer} onChange={setConsumer} exclude={supplier} />
        <TenantSelect id="rel-supplier" label="Tenant fornecedor (presta o serviço)" tenants={tenants} value={supplier} onChange={setSupplier} exclude={consumer} />
        <label className="block space-y-1 text-sm" htmlFor="rel-kind">
          <span className="font-medium text-text-primary">Tipo de serviço</span>
          <input id="rel-kind" value={serviceKind} onChange={(e) => setServiceKind(e.target.value)} placeholder="ex.: trânsito, suporte terceirizado" className="h-9 w-full rounded-control border border-border-light bg-surface px-2 text-sm" />
        </label>
        <label className="block space-y-1 text-sm" htmlFor="rel-ref">
          <span className="font-medium text-text-primary">Referência (opcional)</span>
          <input id="rel-ref" value={serviceRef} onChange={(e) => setServiceRef(e.target.value)} className="h-9 w-full rounded-control border border-border-light bg-surface px-2 text-sm" />
        </label>
        {error && <p role="alert" className="text-sm text-status-danger">{error}</p>}
      </div>
    </Modal>
  );
}

function RevokeRelationshipConfirm({ hubId, relationship, tenantName, open, onClose, onDone }: { hubId: string; relationship: GroupRelationship; tenantName: (id: string) => string; open: boolean; onClose: () => void; onDone: () => void }) {
  const mutation = useMutation({
    mutationFn: () => groupOrganizerAPI.revokeRelationship(hubId, relationship.id),
    onSuccess: onDone,
    onError: (err) => { if (isUnauthorized(err)) handleUnauthorized(); },
  });
  return (
    <ConfirmDialog
      open={open}
      title="Revogar relação comercial"
      message={`A pasta compartilhada ${tenantName(relationship.consumer_tenant_id)} ↔ ${tenantName(relationship.supplier_tenant_id)} deixa de existir. Os vínculos individuais de cada Tenant com seus grupos não são afetados.`}
      confirmLabel="Revogar"
      destructive
      isPending={mutation.isPending}
      onConfirm={() => mutation.mutate()}
      onCancel={onClose}
    />
  );
}
