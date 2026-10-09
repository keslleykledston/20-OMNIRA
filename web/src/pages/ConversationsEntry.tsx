import { useEffect, useMemo } from 'react';
import { useQueries, useQueryClient } from '@tanstack/react-query';
import { useSearchParams } from 'react-router-dom';
import { hubAPI, type HubCompanyOption } from '../lib/hub';
import { useMyHubs } from '../hooks/useMyHubs';
import { useMyTenants } from '../hooks/useMyTenants';
import { getTenantId } from '../lib/session';
import { switchTenant, tenantDisplayName } from '../lib/tenants';
import { LoadingState, Tabs } from '../components/primitives';
import AccessLostNotice from '../components/hub/AccessLostNotice';
import HubInboxPage from './HubInboxPage';
import InboxWorkspace from './InboxWorkspace';

// "Conversas" (ADR-0039, ADR-0040). A person who serves ONE company keeps the full workspace of that company, unchanged. A person
// who can serve TWO OR MORE instances (their own memberships plus whatever the Hub authorized) gets one tab per instance and, when a
// Hub serves them in 2+ instances, an "Todas" tab with every instance's conversations in one inbox. Which instances those are is
// decided by the server from live access, never by this page, and the lists are re-read every few seconds: an instance that
// disappears from them while its tab is open is replaced by a notice and its cached data is dropped.
// An instance the person belongs to opens the full workspace of that instance (a full navigation, the way the instance switcher
// works: caches, realtime streams and per-tenant state start clean). An instance reachable only through the Hub shows the Hub's
// text view of that instance until ADR-0040 gives it the full context.
// If the Hub is off or anything about it fails, the classic workspace is shown: this entry can never make "Conversas" unavailable.
// `?modo=empresa` asks for the classic, single-company workspace (kept for old links).
const REFRESH_MS = 15_000;
const ALL = 'all';
// One automatic switch per instance: if storage refuses the switch the page would otherwise reload forever.
const SWITCH_TRIED = 'omnira.conversas.switchTried';
const switchTarget = (id: string) => `/inbox?instancia=${encodeURIComponent(id)}`;

interface Instance {
  id: string;
  name: string;
  member: boolean;
  hubId?: string;
}

export default function ConversationsEntry() {
  const [params, setParams] = useSearchParams();
  const queryClient = useQueryClient();
  const hubs = useMyHubs();
  const mine = useMyTenants(REFRESH_MS);
  const list = hubs.data ?? [];
  const wantsClassic = params.get('modo') === 'empresa';
  // Every hub the person belongs to is probed (one cheap request each); the companies come with every page, so the first one is enough.
  const probes = useQueries({
    queries: list.map((h) => ({
      queryKey: ['hub-companies-probe', h.id],
      enabled: !wantsClassic,
      queryFn: () => hubAPI.inbox(h.id, undefined, [], 1),
      staleTime: 10_000,
      refetchInterval: REFRESH_MS,
      retry: false,
    })),
  });

  const loading = hubs.isLoading || mine.isLoading || probes.some((p) => p.isLoading);
  // "Lost" is only ever concluded from lists that were read successfully: a failed read is not evidence of anything.
  const listsTrusted = !hubs.isError && !mine.isError && probes.every((p) => !p.isError);

  const { instances, hubWithMany } = useMemo(() => {
    const byId = new Map<string, Instance>();
    for (const t of mine.data ?? []) byId.set(t.id, { id: t.id, name: tenantDisplayName(t), member: true });
    let many = '';
    list.forEach((h, i) => {
      const companies: HubCompanyOption[] = probes[i]?.data?.companies ?? [];
      if (!many && companies.length >= 2) many = h.id;
      for (const c of companies) {
        const known = byId.get(c.id);
        if (known) known.hubId = known.hubId ?? h.id;
        else byId.set(c.id, { id: c.id, name: c.name, member: false, hubId: h.id });
      }
    });
    return { instances: [...byId.values()], hubWithMany: many };
  }, [mine.data, list, probes]);

  const requested = params.get('instancia') ?? '';
  const current = getTenantId();
  const hasAllTab = !!hubWithMany;
  const fallbackTab = hasAllTab ? ALL : instances.some((i) => i.id === current) ? current : (instances[0]?.id ?? ALL);
  const active = requested || fallbackTab;
  const activeInstance = active === ALL ? undefined : instances.find((i) => i.id === active);
  const lost = !loading && listsTrusted && active !== ALL && !activeInstance;

  // The tab to show is one of the person's own instances but not the one the session is on (a deep link, or the session's instance
  // is no longer theirs): switch once, the way a click on the tab does.
  const tabsShown = !wantsClassic && !loading && instances.length >= 2;
  const needsSwitch = tabsShown && !!activeInstance?.member && active !== current;
  const autoSwitchBlocked = (() => {
    try {
      return sessionStorage.getItem(SWITCH_TRIED) === active;
    } catch {
      return true;
    }
  })();
  useEffect(() => {
    try {
      // forgotten only once the tabs are on screen and the session already is on the shown instance (not while loading)
      if (tabsShown && !needsSwitch) sessionStorage.removeItem(SWITCH_TRIED);
    } catch {
      /* storage blocked: the button below is the way */
    }
    if (!needsSwitch || autoSwitchBlocked) return;
    try {
      sessionStorage.setItem(SWITCH_TRIED, active);
    } catch {
      return;
    }
    switchTenant(active, () => window.location.assign(switchTarget(active)));
  }, [tabsShown, needsSwitch, autoSwitchBlocked, active]);

  // Access ended while the tab was open: nothing of that instance stays in the client cache.
  useEffect(() => {
    if (!lost) return;
    queryClient.removeQueries({ predicate: (q) => q.queryKey.some((k) => k === active) });
  }, [lost, active, queryClient]);

  if (wantsClassic) return <InboxWorkspace />;
  if (loading) return <div className="p-6"><LoadingState message="Carregando conversas…" /></div>;

  // One instance (or none the Hub knows): no tabs. This is the behaviour before the tabs existed.
  // (unless the person is looking at an instance they just lost: then the notice must be seen, even if one instance is all that is left)
  if (instances.length < 2 && !lost) {
    const needed = (mine.data?.length ?? 0) === 0 ? 1 : 2;
    const winner = list.findIndex((_, i) => (probes[i]?.data?.companies?.length ?? 0) >= needed);
    return winner >= 0 ? <HubInboxPage hubId={list[winner].id} /> : <InboxWorkspace />;
  }

  const items = [
    ...(hasAllTab ? [{ id: ALL, label: 'Todas' }] : []),
    ...instances.map((i) => ({ id: i.id, label: i.name })),
  ];
  const select = (id: string) => {
    const target = instances.find((i) => i.id === id);
    if (target?.member && id !== current) {
      // another instance of one's own: a full navigation, as the instance switcher does
      switchTenant(id, () => window.location.assign(switchTarget(id)));
      return;
    }
    setParams(id === ALL ? {} : { instancia: id });
  };

  let body: React.ReactNode;
  if (lost) {
    body = <AccessLostNotice />;
  } else if (active === ALL) {
    body = <HubInboxPage key="all" hubId={hubWithMany} embedded />;
  } else if (activeInstance?.member) {
    body =
      active === current ? (
        <InboxWorkspace key={active} />
      ) : autoSwitchBlocked ? (
        <div className="p-6 text-center text-sm text-text-secondary">
          Esta instância não está ativa nesta sessão.{' '}
          <button type="button" className="font-medium text-accent-primary underline-offset-2 hover:underline" onClick={() => select(active)}>
            Abrir a instância
          </button>
        </div>
      ) : (
        <LoadingState message="Abrindo a instância…" />
      );
  } else if (activeInstance?.hubId) {
    body = (
      <HubInboxPage
        key={active}
        hubId={activeInstance.hubId}
        embedded
        onlyCompany={active}
        notice="Você acessa esta instância pelo Hub: aqui há conversas e resposta por texto. Mídia, dados do cliente e chamado no ERP chegam na próxima etapa."
      />
    );
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <Tabs aria-label="Instâncias" items={items} value={items.some((i) => i.id === active) ? active : ''} onChange={select} className="shrink-0 px-2" />
      <div className="min-h-0 flex-1">{body}</div>
    </div>
  );
}
