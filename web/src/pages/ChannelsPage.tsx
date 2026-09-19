import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { channelsAPI, channelErrorMessage, ChannelConnection } from '../lib/channels';
import { getTenantId } from '../lib/session';

const STATUS_STYLE: Record<string, string> = {
  active: 'bg-green-100 text-green-800',
  pending: 'bg-amber-100 text-amber-800',
  disconnected: 'bg-slate-200 text-slate-700',
  failed: 'bg-red-100 text-red-800',
  degraded: 'bg-orange-100 text-orange-800',
  revoked: 'bg-red-100 text-red-800',
};

/**
 * WhatsApp (unofficial, WAHA) connections. Admin only (channel.manage): the backend answers 403
 * to everyone else and this page shows that instead of an empty list.
 */
export default function ChannelsPage() {
  const tenantId = getTenantId();
  const queryClient = useQueryClient();
  const [risk, setRisk] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const list = useQuery({
    queryKey: ['channel-connections', tenantId],
    queryFn: channelsAPI.list,
    enabled: !!tenantId,
    retry: false,
  });

  const create = useMutation({
    mutationFn: channelsAPI.create,
    onSuccess: () => {
      setRisk(false);
      setError(null);
      void queryClient.invalidateQueries({ queryKey: ['channel-connections', tenantId] });
    },
    onError: (err) => setError(channelErrorMessage(err, 'Failed to create the connection')),
  });

  const forbidden = (list.error as any)?.response?.status === 403;

  return (
    <div className="p-6 max-w-3xl mx-auto space-y-6">
      <header>
        <h1 className="text-2xl font-bold text-slate-900">WhatsApp connections</h1>
        <p className="text-slate-600 text-sm mt-1">
          Unofficial WhatsApp Web sessions (WAHA). Pair a number by scanning a QR code with the phone.
        </p>
      </header>

      {forbidden && (
        <div role="alert" className="bg-amber-50 border border-amber-200 text-amber-800 rounded-lg p-4">
          {channelErrorMessage({ response: { status: 403 } })}
        </div>
      )}
      {list.isError && !forbidden && (
        <div role="alert" className="bg-red-50 border border-red-200 text-red-700 rounded-lg p-4">
          {channelErrorMessage(list.error, 'Failed to load connections')}
        </div>
      )}

      {!forbidden && (
        <section className="bg-white rounded-xl shadow p-5 space-y-3">
          <h2 className="font-semibold text-slate-900">New connection</h2>
          <div className="bg-red-50 border border-red-200 text-red-800 text-sm rounded-lg p-3">
            <strong>Risk:</strong> unofficial WhatsApp automation violates WhatsApp's terms and the number can be
            banned. Use a disposable number and never a business-critical one.
          </div>
          <label className="flex items-start gap-2 text-sm text-slate-800">
            <input type="checkbox" checked={risk} onChange={(e) => setRisk(e.target.checked)} className="mt-1" />
            <span>I understand and accept this risk (recorded with my user and the time)</span>
          </label>
          <button
            className="px-4 py-2 rounded-lg bg-blue-600 text-white font-medium disabled:opacity-50"
            disabled={!risk || create.isPending}
            onClick={() => create.mutate()}
          >
            {create.isPending ? 'Creating...' : 'Create connection'}
          </button>
          {error && (
            <div role="alert" className="text-sm text-red-700">
              {error}
            </div>
          )}
        </section>
      )}

      <section className="space-y-4">
        {list.isLoading && <div className="text-slate-500">Loading connections...</div>}
        {list.data?.length === 0 && !forbidden && <div className="text-slate-500">No connections yet.</div>}
        {list.data?.map((c) => (
          <ConnectionCard key={c.id} connection={c} />
        ))}
      </section>
    </div>
  );
}

function ConnectionCard({ connection }: { connection: ChannelConnection }) {
  const queryClient = useQueryClient();
  const tenantId = getTenantId();
  const [watching, setWatching] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const key = ['channel-connection', tenantId, connection.id];

  // Live state: polled only while the operator is pairing (after Start) so we do not hammer WAHA.
  const live = useQuery({
    queryKey: key,
    queryFn: () => channelsAPI.get(connection.id),
    enabled: watching,
    refetchInterval: watching ? 2000 : false,
    retry: false,
  });
  const current = live.data ?? connection;
  const session = live.data?.session_status;

  // QR only exists while the session waits for a scan; WhatsApp rotates it, so refresh often.
  const qr = useQuery({
    queryKey: ['channel-qr', tenantId, connection.id],
    queryFn: () => channelsAPI.qr(connection.id),
    enabled: session === 'needs_qr',
    refetchInterval: session === 'needs_qr' ? 8000 : false,
    retry: false,
  });

  const afterAction = (data: ChannelConnection) => {
    queryClient.setQueryData(key, data);
    void queryClient.invalidateQueries({ queryKey: ['channel-connections', tenantId] });
  };
  const start = useMutation({
    mutationFn: () => channelsAPI.start(connection.id),
    onSuccess: (d) => {
      setError(null);
      setWatching(true);
      afterAction(d);
    },
    onError: (err) => setError(channelErrorMessage(err, 'Failed to start the session')),
  });
  const stop = useMutation({
    mutationFn: () => channelsAPI.stop(connection.id),
    onSuccess: (d) => {
      setError(null);
      afterAction(d);
    },
    onError: (err) => setError(channelErrorMessage(err, 'Failed to stop the session')),
  });

  const connected = current.status === 'active';
  return (
    <article className="bg-white rounded-xl shadow p-5 space-y-3" data-testid={`connection-${connection.id}`}>
      <div className="flex items-center justify-between gap-3 flex-wrap">
        <div>
          <div className="font-mono text-sm text-slate-700">{connection.id.slice(0, 8)}</div>
          <div className="text-xs text-slate-500">WAHA · unofficial</div>
        </div>
        <span className={`px-2 py-1 rounded text-xs font-semibold ${STATUS_STYLE[current.status] ?? 'bg-slate-100'}`} data-testid="conn-status">
          {current.status}
        </span>
      </div>

      {connected && (
        <div className="text-sm text-green-700">
          Connected{current.external_account_id ? ` as +${current.external_account_id}` : ''}
        </div>
      )}
      {session && !connected && <div className="text-sm text-slate-600">Session: {session.replace('_', ' ')}</div>}

      {session === 'needs_qr' && qr.data && (
        <div className="space-y-1">
          <img
            alt="WhatsApp pairing QR code"
            className="w-56 h-56 border rounded"
            src={`data:${qr.data.mimetype};base64,${qr.data.data}`}
          />
          <p className="text-xs text-slate-500">
            On the phone: WhatsApp → Settings → Linked devices → Link a device → scan this code.
          </p>
        </div>
      )}
      {session === 'needs_qr' && !qr.data && <div className="text-sm text-slate-500">Loading QR code...</div>}

      <div className="flex gap-2">
        {!connected && (
          <button
            className="px-3 py-1.5 rounded-lg bg-green-600 text-white text-sm font-medium disabled:opacity-50"
            disabled={start.isPending}
            onClick={() => start.mutate()}
          >
            {start.isPending ? 'Starting...' : watching || session ? 'Restart pairing' : 'Start session'}
          </button>
        )}
        <button
          className="px-3 py-1.5 rounded-lg bg-slate-200 text-slate-800 text-sm font-medium disabled:opacity-50"
          disabled={stop.isPending}
          onClick={() => stop.mutate()}
        >
          Stop
        </button>
      </div>
      {error && (
        <div role="alert" className="text-sm text-red-700">
          {error}
        </div>
      )}
    </article>
  );
}
