import { useEffect, useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  ChannelConnection,
  integrationErrorMessage,
  integrationsAPI,
  ProviderDescriptor,
} from '../lib/integrations';
import { getTenantId } from '../lib/session';
import { QRPairingModal } from '../components/QRPairingModal';

const STATUS_STYLE: Record<string, string> = {
  active: 'bg-green-100 text-green-800',
  pending: 'bg-amber-100 text-amber-800',
  disconnected: 'bg-slate-200 text-slate-700',
  failed: 'bg-red-100 text-red-800',
  degraded: 'bg-orange-100 text-orange-800',
  revoked: 'bg-red-100 text-red-800',
};

const STATUS_LABEL: Record<string, string> = {
  active: 'Conectado', pending: 'Pendente', disconnected: 'Desconectado', failed: 'Falhou',
  degraded: 'Degradado', revoked: 'Revogado',
};

export default function IntegrationsPage() {
  const tenantId = getTenantId();
  const [wizardOpen, setWizardOpen] = useState(false);
  const providers = useQuery({
    queryKey: ['channel-providers', tenantId],
    queryFn: integrationsAPI.providers,
    enabled: !!tenantId,
    retry: false,
    staleTime: 5 * 60_000,
  });
  const connections = useQuery({
    queryKey: ['channel-connections', tenantId],
    queryFn: integrationsAPI.list,
    enabled: !!tenantId,
    retry: false,
  });
  const forbidden = (providers.error as any)?.response?.status === 403 || (connections.error as any)?.response?.status === 403;
  const descriptorByID = useMemo(() => new Map((providers.data ?? []).map((p) => [p.id, p])), [providers.data]);

  return (
    <div className="p-6 max-w-5xl mx-auto space-y-6">
      <header className="flex items-start justify-between gap-4 flex-wrap">
        <div>
          <h1 className="text-2xl font-bold text-slate-900">Integrações</h1>
          <p className="text-slate-600 text-sm mt-1">Conecte canais e, nas próximas fases, ERPs usando um fluxo único e seguro.</p>
        </div>
        {!forbidden && (
          <button className="btn-primary" onClick={() => setWizardOpen(true)}>+ Adicionar integração</button>
        )}
      </header>

      {forbidden && <Alert tone="warning">Somente administradores do tenant gerenciam integrações.</Alert>}
      {(providers.isError || connections.isError) && !forbidden && (
        <Alert tone="error">{integrationErrorMessage(providers.error ?? connections.error, 'Não foi possível carregar as integrações.')}</Alert>
      )}

      {wizardOpen && providers.data && (
        <AddIntegrationWizard providers={providers.data} onClose={() => setWizardOpen(false)} />
      )}

      <section aria-label="Integrações conectadas" className="grid gap-4 md:grid-cols-2">
        {(providers.isLoading || connections.isLoading) && <div className="text-slate-500">Carregando integrações...</div>}
        {connections.data?.length === 0 && !forbidden && (
          <div className="md:col-span-2 border border-dashed border-slate-300 rounded-xl p-8 text-center text-slate-600">
            Nenhuma integração ainda. Conecte seu primeiro canal.
          </div>
        )}
        {connections.data?.map((connection) => (
          <ConnectionCard key={connection.id} connection={connection} descriptor={descriptorByID.get(connection.provider)} />
        ))}
      </section>
    </div>
  );
}

function AddIntegrationWizard({ providers, onClose }: { providers: ProviderDescriptor[]; onClose: () => void }) {
  const tenantId = getTenantId();
  const queryClient = useQueryClient();
  const [selected, setSelected] = useState<ProviderDescriptor | null>(null);
  const [riskAccepted, setRiskAccepted] = useState(false);
  const [created, setCreated] = useState<ChannelConnection | null>(null);
  const [error, setError] = useState<string | null>(null);
  const create = useMutation({
    mutationFn: () => integrationsAPI.create(selected!.id, {}, riskAccepted),
    onSuccess: (connection) => {
      setCreated(connection);
      setError(null);
      void queryClient.invalidateQueries({ queryKey: ['channel-connections', tenantId] });
    },
    onError: (err) => setError(integrationErrorMessage(err)),
  });

  return (
    <section role="dialog" aria-modal="true" aria-labelledby="integration-wizard-title" className="bg-white border border-slate-200 rounded-xl shadow-lg p-5 space-y-4">
      <div className="flex items-center justify-between gap-3">
        <div>
          <h2 id="integration-wizard-title" className="font-semibold text-slate-900">Adicionar integração</h2>
          <p className="text-xs text-slate-500">Passo {created ? 3 : selected ? 2 : 1} de 3</p>
        </div>
        <button className="text-slate-600 hover:text-slate-900" aria-label="Fechar assistente" onClick={onClose}>Fechar</button>
      </div>

      {!selected && (
        <div className="grid gap-3 sm:grid-cols-2">
          {providers.map((provider) => (
            <button
              key={provider.id}
              disabled={!provider.enabled}
              onClick={() => setSelected(provider)}
              className="text-left border rounded-lg p-4 disabled:bg-slate-100 disabled:text-slate-500 enabled:hover:border-blue-500"
            >
              <span className="font-medium block">{provider.name}</span>
              <span className="text-xs uppercase tracking-wide">{provider.kind === 'unofficial' ? 'Não oficial' : 'Oficial'}</span>
              {!provider.enabled && <span className="text-xs block mt-2">{provider.unavailable_reason}</span>}
            </button>
          ))}
        </div>
      )}

      {selected && !created && (
        <div className="space-y-4">
          <button className="text-sm text-blue-700" onClick={() => { setSelected(null); setRiskAccepted(false); }}>← Voltar</button>
          <h3 className="font-medium">{selected.name}</h3>
          {selected.risk_notice && <Alert tone="warning"><strong>Risco:</strong> {selected.risk_notice}</Alert>}
          {selected.kind === 'unofficial' && (
            <label className="flex items-start gap-2 text-sm text-slate-800">
              <input type="checkbox" checked={riskAccepted} onChange={(event) => setRiskAccepted(event.target.checked)} className="mt-1" />
              <span>Entendo e aceito este risco; o aceite será registrado com meu usuário e horário.</span>
            </label>
          )}
          <button
            className="btn-primary"
            disabled={(selected.kind === 'unofficial' && !riskAccepted) || create.isPending}
            onClick={() => create.mutate()}
          >
            {create.isPending ? 'Criando...' : 'Criar conexão'}
          </button>
          {error && <div role="alert" className="text-sm text-red-700">{error}</div>}
        </div>
      )}

      {selected && created && (
        <div className="space-y-3">
          <p className="text-sm text-slate-600">Conexão criada. Inicie a sessão e leia o QR com o telefone.</p>
          <ConnectionCard connection={created} descriptor={selected} pairing onConnected={onClose} />
        </div>
      )}

    </section>
  );
}

function ConnectionCard({ connection, descriptor, pairing = false, onConnected }: {
  connection: ChannelConnection;
  descriptor?: ProviderDescriptor;
  pairing?: boolean;
  onConnected?: () => void;
}) {
  const queryClient = useQueryClient();
  const tenantId = getTenantId();
  const [modalOpen, setModalOpen] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const key = ['channel-connection', tenantId, connection.id];
  // O status é consultado enquanto a conexão não estiver ativa, mesmo sem o modal
  // aberto: antes ele só era buscado após clicar em "Iniciar sessão", então um
  // card recarregado ficava preso no status do fetch da lista.
  const live = useQuery({
    queryKey: key,
    queryFn: () => integrationsAPI.get(connection.id),
    initialData: connection,
    refetchInterval: (query) => (query.state.data?.status === 'active' ? false : 4000),
    retry: false,
  });
  const current = live.data ?? connection;
  const session = current.session_status;
  const connected = current.status === 'active';

  useEffect(() => {
    if (connected) onConnected?.();
  }, [connected, onConnected]);

  const afterAction = (data: ChannelConnection) => {
    queryClient.setQueryData(key, data);
    void queryClient.invalidateQueries({ queryKey: ['channel-connections', tenantId] });
  };
  const start = useMutation({
    mutationFn: () => integrationsAPI.start(connection.id),
    onSuccess: (data) => { setError(null); afterAction(data); setModalOpen(true); },
    onError: (err) => setError(integrationErrorMessage(err, 'Não foi possível iniciar a sessão.')),
  });
  const stop = useMutation({
    mutationFn: () => integrationsAPI.stop(connection.id),
    onSuccess: (data) => { setError(null); setModalOpen(false); afterAction(data); },
    onError: (err) => setError(integrationErrorMessage(err, 'Não foi possível parar a sessão.')),
  });

  const pairable = descriptor?.connect_method !== 'credentials';
  const sessionLive = session === 'needs_qr' || session === 'starting';

  return (
    <article className={`${pairing ? 'border' : 'bg-white shadow'} rounded-xl p-5 space-y-3`} data-testid={`connection-${connection.id}`}>
      <div className="flex items-center justify-between gap-3 flex-wrap">
        <div>
          <div className="font-semibold text-slate-900">{descriptor?.name ?? connection.provider}</div>
          <div className="text-xs text-slate-500">{connection.id.slice(0, 8)} · {connection.provider_kind === 'unofficial' ? 'não oficial' : 'oficial'}</div>
        </div>
        <span className={`px-2 py-1 rounded text-xs font-semibold ${STATUS_STYLE[current.status] ?? 'bg-slate-100'}`} data-testid="conn-status">
          {STATUS_LABEL[current.status] ?? current.status}
        </span>
      </div>
      {connected && <div className="text-sm text-green-700">Conectado{current.external_account_id ? ` como +${current.external_account_id}` : ''}</div>}
      {session && !connected && <div aria-live="polite" className="text-sm text-slate-600">Sessão: {session.replace('_', ' ')}</div>}

      <div className="flex gap-2 flex-wrap">
        {!connected && pairable && (
          <button className="btn-primary" disabled={start.isPending} onClick={() => (sessionLive ? setModalOpen(true) : start.mutate())}>
            {start.isPending ? 'Iniciando...' : sessionLive ? 'Ver QR' : 'Iniciar sessão'}
          </button>
        )}
        {pairable && (
          <button className="btn-secondary" disabled={stop.isPending} onClick={() => stop.mutate()}>Parar</button>
        )}
      </div>
      {error && <div role="alert" className="text-sm text-red-700">{error}</div>}

      {modalOpen && pairable && (
        <QRPairingModal
          connection={current}
          providerName={descriptor?.name ?? connection.provider}
          onClose={() => setModalOpen(false)}
        />
      )}
    </article>
  );
}

function Alert({ tone, children }: { tone: 'warning' | 'error'; children: React.ReactNode }) {
  const classes = tone === 'warning' ? 'bg-amber-50 border-amber-200 text-amber-800' : 'bg-red-50 border-red-200 text-red-700';
  return <div role="alert" className={`border rounded-lg p-4 ${classes}`}>{children}</div>;
}
