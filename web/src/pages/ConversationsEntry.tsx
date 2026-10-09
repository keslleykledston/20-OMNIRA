import { useQueries } from '@tanstack/react-query';
import { useSearchParams } from 'react-router-dom';
import { hubAPI } from '../lib/hub';
import { useMyHubs } from '../hooks/useMyHubs';
import { LoadingState } from '../components/primitives';
import HubInboxPage from './HubInboxPage';
import InboxWorkspace from './InboxWorkspace';

// "Conversas" (ADR-0039). A person who serves ONE company keeps the full workspace of that company, unchanged. A person the
// Hub authorized for TWO OR MORE companies gets, by default, every company's conversations in one inbox with a company filter
// (the same view as /hub). Which companies those are is decided by the server from live grants, never by this page; if the
// Hub is off or anything about it fails, the classic workspace is shown: this entry can never make "Conversas" unavailable.
// `?modo=empresa` asks for the classic, single-company workspace.
export default function ConversationsEntry() {
  const [params] = useSearchParams();
  const hubs = useMyHubs();
  const list = hubs.data ?? [];
  const wantsClassic = params.get('modo') === 'empresa';
  // Every hub the person belongs to is probed (one cheap request each): the first that serves them in 2+ companies wins.
  const probes = useQueries({
    queries: list.map((h) => ({
      queryKey: ['hub-companies-probe', h.id],
      enabled: !wantsClassic,
      queryFn: () => hubAPI.inbox(h.id, undefined, [], 1),
      staleTime: 60_000,
      retry: false,
    })),
  });

  if (wantsClassic) return <InboxWorkspace />;
  if (hubs.isLoading || probes.some((p) => p.isLoading)) return <div className="p-6"><LoadingState message="Carregando conversas…" /></div>;
  const winner = list.findIndex((_, i) => (probes[i]?.data?.companies?.length ?? 0) >= 2);
  return winner >= 0 ? <HubInboxPage unified hubId={list[winner].id} /> : <InboxWorkspace />;
}
