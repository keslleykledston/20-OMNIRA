import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { hubWriteAPI, describeHubWriteError, type HubItemDetail } from '../../lib/hub';
import { handleUnauthorized, isUnauthorized } from '../../lib/session';
import { Button, ErrorState, LoadingState, Modal } from '../primitives';

// Hand the conversation to another person of the hub, or give it back to the queue (ADR-0038 phase 4). The candidates are what the
// SERVER says may answer THIS instance right now; the choice is proven again, in the transaction, for the person picked.
export default function TransferDialog({ hubId, detail, open, onClose, onDone }: {
  hubId: string; detail: HubItemDetail; open: boolean; onClose: () => void; onDone: () => void;
}) {
  const [picked, setPicked] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const candidates = useQuery({
    queryKey: ['hub-transfer-candidates', hubId, detail.item.id],
    enabled: open,
    queryFn: () => hubWriteAPI.candidates(hubId, detail.item.id, detail.tenant.id),
    retry: false,
  });
  const run = async (to: string | null) => {
    setBusy(true);
    setError(null);
    try {
      await hubWriteAPI.transfer(hubId, detail.item.id, detail.tenant.id, to);
      setPicked('');
      onDone();
    } catch (err) {
      if (isUnauthorized(err)) handleUnauthorized();
      else setError(describeHubWriteError(err));
      void candidates.refetch(); // the list may be stale (someone lost access)
    } finally {
      setBusy(false);
    }
  };
  const list = candidates.data ?? [];
  return (
    <Modal open={open} title="Transferir conversa" description="Escolha quem vai continuar este atendimento. A conversa sai da sua lista e fica registrada no histórico." onClose={onClose}
      footer={<>
        <Button variant="secondary" size="sm" onClick={() => void run(null)} disabled={busy}>Devolver à fila</Button>
        <Button variant="secondary" size="sm" onClick={onClose} disabled={busy}>Cancelar</Button>
        <Button size="sm" onClick={() => void run(picked)} disabled={!picked || busy} isLoading={busy}>Transferir</Button>
      </>}>
      {candidates.isLoading && <LoadingState message="Buscando quem pode receber…" />}
      {candidates.isError && !isUnauthorized(candidates.error) && <ErrorState message={describeHubWriteError(candidates.error)} />}
      {candidates.data && list.length === 0 && <p className="text-sm text-text-secondary">Ninguém mais tem acesso para responder esta instância agora. Você pode devolver a conversa à fila.</p>}
      {list.length > 0 && (
        <fieldset>
          <legend className="sr-only">Quem recebe a conversa</legend>
          <ul className="divide-y divide-border-subtle">
            {list.map((c) => (
              <li key={c.user_id}>
                <label className="flex items-center gap-3 py-2 text-sm">
                  <input type="radio" name="transfer-to" value={c.user_id} checked={picked === c.user_id} onChange={() => setPicked(c.user_id)} aria-label={c.name || c.email} />
                  <span className="font-medium text-text-primary">{c.name || c.email}</span>
                  {c.name && <span className="text-text-secondary">{c.email}</span>}
                  <span className="ml-auto text-xs text-text-tertiary">{c.load} abertas</span>
                </label>
              </li>
            ))}
          </ul>
        </fieldset>
      )}
      {error && <p role="alert" className="mt-2 text-sm text-status-danger">{error}</p>}
    </Modal>
  );
}
