import { useEffect } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ChannelConnection, integrationErrorMessage, integrationsAPI } from '../lib/integrations';
import { getTenantId } from '../lib/session';

// O QR do WhatsApp expira em poucos segundos; o backend serve sempre o corrente.
const QR_REFRESH_MS = 5000;
const STATUS_POLL_MS = 2000;

type Phase = 'starting' | 'qr' | 'connected' | 'failed' | 'idle';

function phaseOf(connection: ChannelConnection): Phase {
  if (connection.status === 'active') return 'connected';
  if (connection.status === 'failed' || connection.session_status === 'failed') return 'failed';
  switch (connection.session_status) {
    case 'needs_qr':
      return 'qr';
    case 'starting':
    case 'working':
      return 'starting';
    default:
      return 'idle';
  }
}

export function QRPairingModal({
  connection,
  providerName,
  onClose,
}: {
  connection: ChannelConnection;
  providerName: string;
  onClose: () => void;
}) {
  const tenantId = getTenantId();
  const queryClient = useQueryClient();

  const live = useQuery({
    queryKey: ['channel-connection', tenantId, connection.id],
    queryFn: () => integrationsAPI.get(connection.id),
    initialData: connection,
    refetchInterval: STATUS_POLL_MS,
    retry: false,
  });
  const current = live.data ?? connection;
  const phase = phaseOf(current);

  const qr = useQuery({
    queryKey: ['channel-qr', tenantId, connection.id],
    queryFn: () => integrationsAPI.qr(connection.id),
    enabled: phase === 'qr',
    refetchInterval: phase === 'qr' ? QR_REFRESH_MS : false,
    retry: false,
  });

  const restart = useMutation({
    mutationFn: () => integrationsAPI.start(connection.id),
    onSuccess: (data) => {
      queryClient.setQueryData(['channel-connection', tenantId, connection.id], data);
      void queryClient.invalidateQueries({ queryKey: ['channel-qr', tenantId, connection.id] });
    },
  });

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onClose();
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  // A lista precisa refletir o novo estado quando o modal fecha.
  useEffect(() => {
    return () => {
      void queryClient.invalidateQueries({ queryKey: ['channel-connections', tenantId] });
    };
  }, [queryClient, tenantId]);

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/60 p-4"
      onClick={(e) => e.target === e.currentTarget && onClose()}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="qr-modal-title"
        className="bg-white rounded-2xl shadow-2xl w-full max-w-md overflow-hidden"
      >
        <header className="flex items-start justify-between gap-3 px-6 pt-5 pb-4 border-b border-slate-100">
          <div>
            <h2 id="qr-modal-title" className="text-lg font-semibold text-slate-900">
              Conectar {providerName}
            </h2>
            <p className="text-xs text-slate-500 mt-0.5">{connection.id.slice(0, 8)}</p>
          </div>
          <button
            onClick={onClose}
            aria-label="Fechar"
            className="text-slate-400 hover:text-slate-700 text-xl leading-none px-1"
          >
            ×
          </button>
        </header>

        <div className="px-6 py-6">
          <div className="flex justify-center mb-5">
            <StatusPill phase={phase} accountId={current.external_account_id} />
          </div>

          {phase === 'connected' && (
            <div className="text-center space-y-4 py-4" data-testid="qr-connected">
              <div className="mx-auto w-16 h-16 rounded-full bg-green-100 flex items-center justify-center text-3xl text-green-700">
                ✓
              </div>
              <p className="text-slate-700">
                Aparelho conectado
                {current.external_account_id ? ` como +${current.external_account_id}` : ''}.
              </p>
              <button className="btn-primary w-full" onClick={onClose}>
                Concluir
              </button>
            </div>
          )}

          {phase === 'qr' && (
            <div className="space-y-4" data-testid="qr-ready">
              <div className="flex justify-center">
                {qr.data ? (
                  <img
                    alt="QR Code para parear o WhatsApp"
                    data-testid="qr-image"
                    className="w-60 h-60 rounded-lg border border-slate-200"
                    src={`data:${qr.data.mimetype};base64,${qr.data.data}`}
                  />
                ) : (
                  <div className="w-60 h-60 rounded-lg border border-dashed border-slate-300 flex items-center justify-center text-sm text-slate-500">
                    {qr.isError ? integrationErrorMessage(qr.error) : 'Gerando QR...'}
                  </div>
                )}
              </div>
              <ol className="text-sm text-slate-600 list-decimal pl-5 space-y-1">
                <li>Abra o WhatsApp no celular.</li>
                <li>
                  Toque em <strong>Configurações</strong> → <strong>Aparelhos conectados</strong>.
                </li>
                <li>
                  Toque em <strong>Conectar aparelho</strong> e aponte a câmera para este código.
                </li>
              </ol>
              <p className="text-xs text-center text-slate-400">
                O código se renova sozinho a cada poucos segundos.
              </p>
            </div>
          )}

          {phase === 'starting' && (
            <div className="text-center py-10 text-slate-600" data-testid="qr-starting">
              Iniciando a sessão no gateway...
            </div>
          )}

          {(phase === 'failed' || phase === 'idle') && (
            <div className="text-center space-y-4 py-6" data-testid="qr-failed">
              <p className="text-sm text-slate-600">
                {phase === 'failed'
                  ? 'A sessão falhou no gateway. Gere um novo QR para tentar de novo.'
                  : 'A sessão ainda não foi iniciada.'}
              </p>
              <button
                className="btn-primary w-full"
                disabled={restart.isPending}
                onClick={() => restart.mutate()}
              >
                {restart.isPending ? 'Iniciando...' : 'Gerar novo QR'}
              </button>
              {restart.isError && (
                <div role="alert" className="text-sm text-red-700">
                  {integrationErrorMessage(restart.error, 'Não foi possível iniciar a sessão.')}
                </div>
              )}
            </div>
          )}
        </div>

        {phase === 'qr' && (
          <footer className="px-6 py-3 bg-slate-50 border-t border-slate-100 flex justify-end">
            <button
              className="text-sm text-blue-700 hover:text-blue-900 disabled:text-slate-400"
              disabled={restart.isPending}
              onClick={() => restart.mutate()}
            >
              {restart.isPending ? 'Reiniciando...' : 'Reiniciar sessão'}
            </button>
          </footer>
        )}
      </div>
    </div>
  );
}

function StatusPill({ phase, accountId }: { phase: Phase; accountId?: string }) {
  const map: Record<Phase, { label: string; className: string }> = {
    connected: { label: accountId ? `Conectado · +${accountId}` : 'Conectado', className: 'bg-green-100 text-green-800' },
    qr: { label: 'Aguardando leitura do QR', className: 'bg-amber-100 text-amber-800' },
    starting: { label: 'Iniciando sessão', className: 'bg-blue-100 text-blue-800' },
    failed: { label: 'Falha na sessão', className: 'bg-red-100 text-red-800' },
    idle: { label: 'Sessão parada', className: 'bg-slate-200 text-slate-700' },
  };
  const { label, className } = map[phase];
  return (
    <span
      aria-live="polite"
      data-testid="qr-modal-status"
      className={`inline-flex items-center gap-2 px-3 py-1.5 rounded-full text-sm font-medium ${className}`}
    >
      <span className="w-2 h-2 rounded-full bg-current opacity-70" />
      {label}
    </span>
  );
}
