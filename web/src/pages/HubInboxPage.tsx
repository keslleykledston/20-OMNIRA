import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query';
import clsx from 'clsx';
import { hubAPI, type HubCompanyOption, type HubInboxItem, type HubItemDetail, type HubSegmentCounts } from '../lib/hub';
import { enterDelegatedInstance, switchTenant } from '../lib/tenants';
import { useDebounced } from '../hooks/useDebounced';
import { handleUnauthorized, isUnauthorized } from '../lib/session';
import { loadTabs, saveTabs, MAX_TABS } from '../lib/hubTabs';
import { useMyHubs } from '../hooks/useMyHubs';
import { useMyTenants } from '../hooks/useMyTenants';
import { useMediaQuery } from '../hooks/useMediaQuery';
import { Drawer, EmptyState, ErrorState, LoadingState } from '../components/primitives';
import HubInboxList from '../components/hub/HubInboxList';
import HubItemView from '../components/hub/HubItemView';
import CompanyFilter from '../components/hub/CompanyFilter';
import HubInboxToolbar, { segmentOf, type HubView } from '../components/hub/HubInboxToolbar';
import HubContextPane from '../components/hub/HubContextPane';
import HubOpenTabs, { type OpenAttendance } from '../components/hub/HubOpenTabs';
import { Icon } from '../components/primitives';

// "Conversas" for a person the Hub authorized for two or more companies (ADR-0039): ONE inbox across every company they are
// authorized to serve, with a company filter where a single-company person has the channel selector. What appears here is
// decided by the server from their live grants; nothing in this page chooses a tenant. Replying needs a reply-capable grant.
// It is chosen by ConversationsEntry, not by a menu entry of its own (the old "Hub" item was the same inbox under another name).
// `embedded` is the page living inside the Conversas tabs: the tabs are how one reaches an instance's full inbox, so the link to it
// is not shown. `onlyCompany` pins the list to ONE instance (the tab of an instance the person reaches only through the Hub): the
// title row and the company filter go away, the tab already says which instance it is. `notice` is a line shown above the list.
function emptyText(view: HubView, search: string, filtered: boolean): string {
  if (search) return 'Nada encontrado para esta busca.';
  if (view === 'closed') return 'Aqui ficam os atendimentos finalizados.';
  if (view === 'waiting') return filtered ? 'Não há conversas na fila das instâncias selecionadas.' : 'Nenhuma conversa pendente: quando um cliente escrever e ninguém tiver assumido, ela aparece aqui.';
  if (view === 'mine') return 'Você não está com nenhuma conversa agora. Assuma uma da fila de pendentes.';
  return filtered ? 'Não há conversas nas instâncias selecionadas.' : 'Você não tem acesso delegado a nenhuma instância neste Hub, ou ainda não há conversas.';
}

export default function HubInboxPage({ hubId: preferredHub = '', embedded = false, onlyCompany = '', notice }: { hubId?: string; embedded?: boolean; onlyCompany?: string; notice?: React.ReactNode }) {
  const queryClient = useQueryClient();
  const hubs = useMyHubs();
  const myTenants = useMyTenants();
  const [filter, setFilter] = useState<{ hub: string; ids: string[] }>({ hub: '', ids: [] });
  const list = hubs.data ?? [];
  const [pickedHub, setPickedHub] = useState(preferredHub);
  // The selection remembers which hub it belongs to, so an item id can never be used against another hub (not even for one render).
  const [selection, setSelection] = useState({ hub: '', id: '' });
  const isMobile = useMediaQuery('(max-width: 767px)');
  // Three panes (queue | conversation | details) only when the room left AFTER the app sidebar can hold them, the same rule as the company
  // workspace (InboxWorkspace): below 1440px the details open in a drawer instead of squeezing the conversation.
  const wide = useMediaQuery('(min-width: 1440px)', true);
  const [showContext, setShowContext] = useState(true);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [view, setView] = useState<HubView>('all');
  const [search, setSearch] = useState('');
  const debouncedSearch = useDebounced(search.trim(), 300);
  const hub = list.find((h) => h.id === pickedHub) ?? list[0];
  const hubId = hub?.id ?? '';
  const selected = selection.hub === hubId ? selection.id : '';
  // The attendances opened as tabs (names the server showed in the list; they authorize nothing). Like the selection they belong to ONE hub.
  const [opened, setOpened] = useState<{ hub: string; tabs: OpenAttendance[] }>({ hub: '', tabs: [] });
  const allOpenTabs = opened.hub === hubId ? opened.tabs : [];
  const setSelected = (id: string, from?: HubInboxItem) => {
    setSelection({ hub: hubId, id });
    if (id && from) {
      setOpened((cur) => {
        const tabs = cur.hub === hubId ? cur.tabs : [];
        if (tabs.some((t) => t.id === id)) return { hub: hubId, tabs };
        // a bounded set: past 8 the oldest tab that is not the one being opened is forgotten
        return { hub: hubId, tabs: [...tabs, { id, name: from.customer_name || 'Sem nome', tenantName: from.tenant_name || 'Instância não identificada', tenantId: from.tenant_id }].slice(-MAX_TABS) };
      });
    }
  };
  // Restore the attendances that were open before a reload (ids only were kept, lib/hubTabs.ts): each is asked of the server again, so what comes back is what the
  // person may read NOW (staleTime 0: a cached copy is never trusted here, a revoked access must not come back from memory), with the names the server says; an attendance that cannot be opened any more is simply not brought back. `restored` also keeps the
  // saving below from overwriting what was kept with the empty state of the first render.
  const [restored, setRestored] = useState('');
  useEffect(() => {
    if (!hubId || restored === hubId) return;
    const saved = loadTabs(hubId);
    if (saved.ids.length === 0) {
      setRestored(hubId);
      return;
    }
    let live = true;
    void Promise.allSettled(
      saved.ids.map((id) => queryClient.fetchQuery({ queryKey: ['hub-item', hubId, id], queryFn: () => hubAPI.item(hubId, id), staleTime: 0 })),
    ).then((results) => {
      if (!live) return;
      const back: OpenAttendance[] = [];
      results.forEach((r, i) => {
        if (r.status === 'fulfilled') back.push({ id: saved.ids[i]!, name: r.value.item.customer_name || 'Sem nome', tenantName: r.value.tenant.name || 'Instância não identificada', tenantId: r.value.tenant.id });
      });
      setOpened((cur) => {
        const already = cur.hub === hubId ? cur.tabs : [];
        const merged = [...back.filter((b) => !already.some((a) => a.id === b.id)), ...already].slice(-MAX_TABS);
        return { hub: hubId, tabs: merged };
      });
      setSelection((cur) => (cur.hub === hubId && cur.id ? cur : { hub: hubId, id: back.some((b) => b.id === saved.selected) ? saved.selected : '' }));
      setRestored(hubId);
    });
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hubId]);
  const tabKey = allOpenTabs.map((t) => t.id).join(',');
  useEffect(() => {
    if (!hubId || restored !== hubId) return;
    const ids = tabKey ? tabKey.split(',') : [];
    saveTabs(hubId, { ids, selected: ids.includes(selected) ? selected : '' });
  }, [hubId, restored, tabKey, selected]);
  // like the selection, the filter belongs to ONE hub
  const companyIds = onlyCompany ? [onlyCompany] : filter.hub === hubId ? filter.ids : [];
  const openTabs = companyIds.length > 0 ? allOpenTabs.filter((t) => companyIds.includes(t.tenantId)) : allOpenTabs;
  const closeTab = (id: string) => {
    const idx = openTabs.findIndex((t) => t.id === id);
    const rest = openTabs.filter((t) => t.id !== id);
    setOpened({ hub: hubId, tabs: allOpenTabs.filter((t) => t.id !== id) });
    if (id === selected) setSelection({ hub: hubId, id: rest[Math.min(idx, rest.length - 1)]?.id ?? '' });
  };

  const inbox = useInfiniteQuery({
    queryKey: ['hub-inbox', hubId, companyIds, view, debouncedSearch],
    enabled: !!hubId,
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) => hubAPI.inbox(hubId, pageParam, companyIds, 30, segmentOf(view), debouncedSearch, 'counts'),
    getNextPageParam: (last) => (last.has_more ? last.next_cursor : undefined),
    refetchInterval: 30_000,
    retry: false,
  });
  // The offered companies come with every page (the same live grants); the first page is enough. While a narrower request is
  // in flight there is no page yet: keep the last known list so the filter (and its open menu) does not vanish under the user's hand.
  const lastCompanies = useRef<{ hub: string; list: HubCompanyOption[] }>({ hub: '', list: [] });
  const fresh = inbox.data?.pages[0]?.companies;
  if (fresh) lastCompanies.current = { hub: hubId, list: fresh };
  const companies: HubCompanyOption[] = lastCompanies.current.hub === hubId ? lastCompanies.current.list : [];
  // A company the server no longer offers (access revoked, contract ended) takes its attendance tabs with it: the names the tabs hold are not kept past the access.
  useEffect(() => {
    if (!fresh) return;
    const ids = new Set(fresh.map((c) => c.id));
    const gone = allOpenTabs.filter((t) => !ids.has(t.tenantId));
    if (gone.length === 0) return;
    setOpened({ hub: hubId, tabs: allOpenTabs.filter((t) => ids.has(t.tenantId)) });
    // what was loaded of those attendances is not kept either (the cache would keep rendering messages the person may no longer read), nor is the selection
    for (const t of gone) queryClient.removeQueries({ queryKey: ['hub-item', hubId, t.id] });
    if (gone.some((t) => t.id === selected)) setSelection({ hub: hubId, id: '' });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fresh, hubId]);
  // The tabs' counters come with the first page; keep the last known ones while a narrower request is in flight so they do not blink.
  const lastCounts = useRef<{ hub: string; counts?: HubSegmentCounts }>({ hub: '' });
  const freshCounts = inbox.data?.pages[0]?.counts;
  if (freshCounts) lastCounts.current = { hub: hubId, counts: freshCounts };
  const counts = lastCounts.current.hub === hubId ? lastCounts.current.counts : undefined;
  const items = useMemo(() => {
    const seen = new Set<string>();
    const out: HubInboxItem[] = [];
    for (const page of inbox.data?.pages ?? []) for (const it of page.items) if (!seen.has(it.id)) { seen.add(it.id); out.push(it); }
    return out;
  }, [inbox.data]);

  const detail = useQuery({
    queryKey: ['hub-item', hubId, selected],
    enabled: !!hubId && !!selected,
    queryFn: () => hubAPI.item(hubId, selected),
    refetchInterval: 15_000,
    retry: false,
  });

  useEffect(() => {
    if ([hubs.error, inbox.error, detail.error].some(isUnauthorized)) handleUnauthorized();
  }, [hubs.error, inbox.error, detail.error]);
  // an attendance that can no longer be opened (access ended, gone) does not stay as a tab
  useEffect(() => {
    if (detail.isError && !isUnauthorized(detail.error) && selected) {
      setOpened((cur) => (cur.hub === hubId ? { hub: hubId, tabs: cur.tabs.filter((t) => t.id !== selected) } : cur));
    }
  }, [detail.isError, detail.error, selected, hubId]);

  // The full workspace of a company opens only when the server said so (companies[].full_context); the click only navigates, and every request of
  // that workspace is decided again by the server.
  const fullContextOf = (d: HubItemDetail) => !!companies.find((c) => c.id === d.tenant.id)?.full_context;
  const openFull = (d: HubItemDetail) => {
    const target = `/inbox?instancia=${encodeURIComponent(d.tenant.id)}&conversation_id=${encodeURIComponent(d.conversation.id)}`;
    const go = () => window.location.assign(target);
    // an instance of the person's own is entered as themselves; any other only as an agent of this Hub
    if (myTenants.data?.some((t) => t.id === d.tenant.id)) switchTenant(d.tenant.id, go);
    else enterDelegatedInstance(d.tenant.id, hubId, d.tenant.name, go);
  };

  if (hubs.isLoading) return <div className="p-6"><LoadingState message="Carregando o Hub…" /></div>;
  if (hubs.isError) {
    return (
      <div className="p-6">
        <ErrorState message="Não foi possível carregar o Hub." action={{ label: 'Tentar novamente', onClick: () => void hubs.refetch() }} />
      </div>
    );
  }
  if (!hub) {
    return (
      <div className="p-6">
        <EmptyState title="Hub indisponível" description="Você não faz parte de nenhum Hub, ou o recurso não está habilitado neste ambiente." />
      </div>
    );
  }

  const scopeLabel = companyIds.length === 0 ? 'Todas as instâncias' : companyIds.length === 1 ? (companies.find((c) => c.id === companyIds[0])?.name ?? '1 instância') : `${companyIds.length} instâncias`;
  const showList = !isMobile || !selected;
  const showDetail = !isMobile || !!selected;
  return (
    <div className="flex h-full min-h-0 flex-col">
      {notice && <div role="note" className="border-b border-border-subtle bg-surface-muted px-4 py-2 text-[12px] text-text-secondary">{notice}</div>}
      {!onlyCompany && (
        <div className="flex flex-wrap items-center gap-3 border-b border-border-subtle px-4 py-3">
          <h1 className="text-lg font-semibold text-text-primary">Conversas</h1>
          {companies.length > 1 && (
            <CompanyFilter companies={companies} value={companyIds} onChange={(ids) => { setFilter({ hub: hubId, ids }); setSelection({ hub: hubId, id: '' }); }} />
          )}
          {list.length > 1 ? (
            <label className="flex items-center gap-2 text-sm text-text-secondary">
              <span className="sr-only">Hub</span>
              <select aria-label="Trocar de Hub" value={hubId} onChange={(e) => setPickedHub(e.target.value)} className="h-9 rounded-control border border-border-light bg-surface px-2 text-sm font-medium text-text-primary">
                {list.map((h) => <option key={h.id} value={h.id}>{h.name}</option>)}
              </select>
            </label>
          ) : (
            <span className="text-sm text-text-secondary">{hub.name}</span>
          )}
          <div className="ml-auto flex items-center gap-3">
            {!embedded && (myTenants.data?.length ?? 0) > 0 && (
              <Link to="/inbox?modo=empresa" className="text-sm text-text-secondary underline-offset-2 hover:underline">Caixa completa de uma instância</Link>
            )}
          </div>
        </div>
      )}
      {/* Which company the person is acting for, said above the panes like the company tabs of the approved design */}
      <div className="flex min-h-9 shrink-0 items-center gap-2 border-b border-border-subtle bg-surface-muted px-4 py-2 text-[11px]">
        <Icon name="supervisor" size={14} className="shrink-0 text-accent-primary" />
        <span className="min-w-0 truncate font-medium text-text-primary">
          {detail.data && selected ? `Contexto ativo: ${detail.data.tenant.name || detail.data.item.tenant_name || 'Instância não identificada'}` : `Caixa: ${scopeLabel}`}
        </span>
        <span className="ml-auto hidden shrink-0 text-text-secondary sm:inline">{items.length}{inbox.hasNextPage ? '+' : ''} {items.length === 1 ? 'conversa' : 'conversas'}</span>
      </div>
      <div
        className={clsx(
          'grid min-h-0 flex-1 grid-cols-1 grid-rows-[minmax(0,1fr)] md:grid-cols-[360px_minmax(0,1fr)]',
          wide && showContext && detail.data && !detail.isError && selected && 'min-[1440px]:grid-cols-[360px_minmax(0,1fr)_320px]',
        )}
      >
        {showList && (
          <section aria-label="Caixa do Hub" className="flex min-h-0 flex-col border-r border-border-subtle">
            <HubInboxToolbar view={view} onViewChange={setView} search={search} onSearchChange={setSearch} counts={counts} />
            <div className="min-h-0 flex-1">
              {inbox.isLoading && <LoadingState message="Carregando conversas…" />}
              {inbox.isError && <div className="p-4"><ErrorState message="Não foi possível carregar as conversas." action={{ label: 'Tentar novamente', onClick: () => void inbox.refetch() }} /></div>}
              {!inbox.isLoading && !inbox.isError && items.length === 0 && (
                <EmptyState title="Nenhuma conversa" description={emptyText(view, debouncedSearch, companyIds.length > 0)} />
              )}
              {items.length > 0 && (
                <HubInboxList items={items} selectedId={selected} onSelect={(id) => setSelected(id, items.find((i) => i.id === id))} hasMore={!!inbox.hasNextPage} loadingMore={inbox.isFetchingNextPage} onLoadMore={() => void inbox.fetchNextPage()} />
              )}
            </div>
          </section>
        )}
        {showDetail && (
          <section aria-label="Conversa selecionada" className="flex min-h-0 min-w-0 flex-col">
            <HubOpenTabs tabs={openTabs} selectedId={selected} onSelect={(id) => setSelected(id)} onClose={closeTab} />
            <div className="min-h-0 flex-1">
            {!selected && <EmptyState title="Selecione uma conversa" description="O nome da instância aparece em cada conversa." />}
            {selected && detail.isLoading && <LoadingState message="Abrindo a conversa…" />}
            {selected && detail.isError && !isUnauthorized(detail.error) && (
              <div className="p-4">
                <ErrorState
                  title="Não foi possível abrir"
                  message="Esta conversa não está disponível: o acesso pode ter sido encerrado ou ela não existe mais."
                  action={{ label: 'Voltar à lista', onClick: () => { setSelected(''); void inbox.refetch(); } }}
                />
              </div>
            )}
            {selected && detail.data && !detail.isError && (
              <HubItemView
                hubId={hubId}
                detail={detail.data}
                onBack={isMobile ? () => setSelected('') : undefined}
                onToggleContext={() => (wide ? setShowContext(!showContext) : setDrawerOpen(true))}
              />
            )}
            </div>
          </section>
        )}
        {/* The details of the attendance: a third column when there is room, otherwise a drawer opened from the conversation header */}
        {wide && showContext && selected && detail.data && !detail.isError && (
          <div className="flex min-h-0 min-w-0 flex-col overflow-hidden border-l border-border-subtle">
            <HubContextPane detail={detail.data} fullContext={fullContextOf(detail.data)} onOpenFull={() => openFull(detail.data!)} />
          </div>
        )}
      </div>
      {!wide && (
        <Drawer open={drawerOpen && !!selected && !!detail.data && !detail.isError} title="Detalhes do atendimento" onClose={() => setDrawerOpen(false)}>
          {detail.data && <HubContextPane detail={detail.data} fullContext={fullContextOf(detail.data)} onOpenFull={() => openFull(detail.data!)} />}
        </Drawer>
      )}
    </div>
  );
}
