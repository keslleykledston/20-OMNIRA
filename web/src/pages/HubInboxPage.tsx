import { useEffect, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { useInfiniteQuery, useQuery } from '@tanstack/react-query';
import { hubAPI, type HubCompanyOption, type HubInboxItem } from '../lib/hub';
import { handleUnauthorized, isUnauthorized } from '../lib/session';
import { useMyHubs } from '../hooks/useMyHubs';
import { useMyTenants } from '../hooks/useMyTenants';
import { useMediaQuery } from '../hooks/useMediaQuery';
import { EmptyState, ErrorState, LoadingState } from '../components/primitives';
import HubInboxList from '../components/hub/HubInboxList';
import HubItemView from '../components/hub/HubItemView';
import CompanyFilter from '../components/hub/CompanyFilter';

// The Hub workspace: ONE inbox across every company the signed-in operator is authorized to serve. What appears here is
// decided by the server from their live grants; nothing in this page chooses a tenant. Replying needs a reply-capable grant.
// `unified` is the same view offered as "Conversas" to a person who serves two or more companies (ADR-0039): the company
// filter takes the place of the channel selector and the default is every company they are authorized to serve.
export default function HubInboxPage({ unified = false, hubId: preferredHub = '' }: { unified?: boolean; hubId?: string }) {
  const hubs = useMyHubs();
  const myTenants = useMyTenants();
  const [filter, setFilter] = useState<{ hub: string; ids: string[] }>({ hub: '', ids: [] });
  const list = hubs.data ?? [];
  const [pickedHub, setPickedHub] = useState(preferredHub);
  // The selection remembers which hub it belongs to, so an item id can never be used against another hub (not even for one render).
  const [selection, setSelection] = useState({ hub: '', id: '' });
  const isMobile = useMediaQuery('(max-width: 767px)');
  const hub = list.find((h) => h.id === pickedHub) ?? list[0];
  const hubId = hub?.id ?? '';
  const selected = selection.hub === hubId ? selection.id : '';
  const setSelected = (id: string) => setSelection({ hub: hubId, id });
  // like the selection, the filter belongs to ONE hub
  const companyIds = filter.hub === hubId ? filter.ids : [];

  const inbox = useInfiniteQuery({
    queryKey: ['hub-inbox', hubId, companyIds],
    enabled: !!hubId,
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) => hubAPI.inbox(hubId, pageParam, companyIds),
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

  const showList = !isMobile || !selected;
  const showDetail = !isMobile || !!selected;
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex flex-wrap items-center gap-3 border-b border-border-subtle px-4 py-3">
        <h1 className="text-lg font-semibold text-text-primary">{unified ? 'Conversas' : 'Hub'}</h1>
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
          {unified && (myTenants.data?.length ?? 0) > 0 && (
            <Link to="/inbox?modo=empresa" className="text-sm text-text-secondary underline-offset-2 hover:underline">Caixa completa de uma empresa</Link>
          )}
          {hub.can_manage_access && (
            <Link to="/acessos" className="text-sm font-medium text-accent-primary underline-offset-2 hover:underline">Acessos</Link>
          )}
          {hub.can_manage_companies && (
            <Link to="/hub/empresas" className="text-sm font-medium text-accent-primary underline-offset-2 hover:underline">Empresas</Link>
          )}
        </div>
      </div>
      <div className="grid min-h-0 flex-1 grid-cols-1 md:grid-cols-[360px_minmax(0,1fr)]">
        {showList && (
          <section aria-label="Caixa do Hub" className="min-h-0 border-r border-border-subtle">
            {inbox.isLoading && <LoadingState message="Carregando conversas…" />}
            {inbox.isError && <div className="p-4"><ErrorState message="Não foi possível carregar as conversas." action={{ label: 'Tentar novamente', onClick: () => void inbox.refetch() }} /></div>}
            {!inbox.isLoading && !inbox.isError && items.length === 0 && (
              <EmptyState title="Nenhuma conversa" description={companyIds.length > 0 ? 'Não há conversas nas empresas selecionadas.' : 'Você não tem acesso delegado a nenhuma empresa neste Hub, ou ainda não há conversas.'} />
            )}
            {items.length > 0 && (
              <HubInboxList items={items} selectedId={selected} onSelect={setSelected} hasMore={!!inbox.hasNextPage} loadingMore={inbox.isFetchingNextPage} onLoadMore={() => void inbox.fetchNextPage()} />
            )}
          </section>
        )}
        {showDetail && (
          <section aria-label="Conversa selecionada" className="min-h-0">
            {!selected && <EmptyState title="Selecione uma conversa" description="O nome da empresa aparece em cada conversa." />}
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
            {selected && detail.data && <HubItemView hubId={hubId} detail={detail.data} onBack={isMobile ? () => setSelected('') : undefined} />}
          </section>
        )}
      </div>
    </div>
  );
}
