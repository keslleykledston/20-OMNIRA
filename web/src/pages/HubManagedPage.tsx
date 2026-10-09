import { useEffect } from 'react';
import { Link, useParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { hubManageAPI, MANAGEMENT_SCOPE_LABEL, type ManagedInstance } from '../lib/hub';
import { handleUnauthorized, isUnauthorized } from '../lib/session';
import { useMyHubs } from '../hooks/useMyHubs';
import { EmptyState, ErrorState, LoadingState } from '../components/primitives';
import { HubChannelScope } from '../features/channels/ChannelScope';
import ChannelsPage from './ChannelsPage';
import WahaWizardPage from './WahaWizardPage';

// "Canais das instâncias" (ADR-0038 phase 3): the instances a person may manage through a Hub, and for each the SAME channels and
// integrations screens the instance's own administrators use, pointed at the Hub's management routes. What is offered here is only a
// hint: the server proves hub -> contract -> role/grant -> scope on every call and answers a uniform 404 to anything else.
export default function HubManagedPage() {
  const hubs = useMyHubs();
  const mine = (hubs.data ?? []).filter((h) => h.can_manage_instances);
  const lists = useQuery({
    queryKey: ['hub-managed', mine.map((h) => h.id)],
    enabled: mine.length > 0,
    queryFn: async () => Promise.all(mine.map(async (h) => ({ hub: h, items: await hubManageAPI.managed(h.id) }))),
    retry: false,
  });
  useEffect(() => {
    if ([hubs.error, lists.error].some(isUnauthorized)) handleUnauthorized();
  }, [hubs.error, lists.error]);

  if (hubs.isLoading || lists.isLoading) return <div className="p-6"><LoadingState message="Carregando…" /></div>;
  if (mine.length === 0) {
    return (
      <div className="p-6">
        <EmptyState title="Nenhuma instância para gerenciar" description="O Hub ainda não delegou a você a gestão de canais ou integrações de nenhuma instância." />
      </div>
    );
  }
  if (lists.isError) return <div className="p-6"><ErrorState message="Não foi possível carregar as instâncias." action={{ label: 'Tentar novamente', onClick: () => void lists.refetch() }} /></div>;
  return (
    <div className="mx-auto w-full max-w-4xl space-y-6 p-4">
      <header>
        <h1 className="text-lg font-semibold text-text-primary">Canais das instâncias</h1>
        <p className="text-sm text-text-secondary">Instâncias cujos canais e integrações o Hub delegou a você. Cada ação fica registrada com o seu nome e o do Hub.</p>
      </header>
      {(lists.data ?? []).map(({ hub, items }) => (
        <section key={hub.id} aria-label={hub.name} className="space-y-2">
          {(lists.data?.length ?? 0) > 1 && <h2 className="text-sm font-medium text-text-secondary">{hub.name}</h2>}
          <ul className="divide-y divide-border-subtle rounded-sheet border border-border-subtle bg-surface">
            {items.map((i) => (
              <li key={i.tenant_id} className="flex flex-wrap items-center gap-3 px-4 py-3">
                <span className="font-medium text-text-primary">{i.name}</span>
                <span className="text-sm text-text-secondary">{i.scopes.map((s) => MANAGEMENT_SCOPE_LABEL[s] ?? s).join(' · ')}</span>
                <Link to={`/instancias/${hub.id}/${i.tenant_id}/canais`} className="ml-auto text-sm font-medium text-accent-primary underline-offset-2 hover:underline">
                  Abrir canais e integrações
                </Link>
              </li>
            ))}
            {items.length === 0 && <li className="px-4 py-3 text-sm text-text-secondary">Nenhuma instância.</li>}
          </ul>
        </section>
      ))}
    </div>
  );
}

function useInstanceName(hubId: string, tenantId: string): string | undefined {
  const managed = useQuery({ queryKey: ['hub-managed-one', hubId], queryFn: () => hubManageAPI.managed(hubId), retry: false });
  return (managed.data ?? []).find((i: ManagedInstance) => i.tenant_id === tenantId)?.name;
}

/** The channels screen of ONE managed instance. The hub and tenant in the address are claims; the server decides. */
export function HubManagedChannelsRoute({ wizard = false }: { wizard?: boolean }) {
  const { hubId = '', tenantId = '' } = useParams();
  const name = useInstanceName(hubId, tenantId);
  return (
    <HubChannelScope hubId={hubId} tenantId={tenantId} instanceName={name ?? 'instância'}>
      <div className="px-6 pt-4 lg:px-8">
        <Link to="/instancias" className="text-sm text-text-secondary underline-offset-2 hover:underline">← Canais das instâncias</Link>
      </div>
      {wizard ? <WahaWizardPage /> : <ChannelsPage />}
    </HubChannelScope>
  );
}
