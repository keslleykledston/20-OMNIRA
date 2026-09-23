import React, { useState, useEffect } from 'react';
import axios from 'axios';
import clsx from 'clsx';
import { API_BASE } from '../lib/config';
import { authHeaders, getTenantId } from '../lib/session';
import { Button } from './primitives';

interface Ticket {
  id: string;
  status: string;
  subject: string;
  created_at: string;
  updated_at: string;
}

interface Company {
  id: string;
  name: string;
  cnpj?: string;
}

interface TicketPanelProps {
  conversationId: string;
  crmContactId?: string;
}

const STATUS_CLASS: Record<string, string> = {
  open: 'border-status-info-border bg-status-info-soft text-status-info',
  in_progress: 'border-status-warning-border bg-status-warning-soft text-status-warning',
  resolved: 'border-status-success-border bg-status-success-soft text-status-success',
  closed: 'border-border-subtle bg-status-muted text-text-secondary',
};

const FIELD =
  'w-full min-w-0 rounded-control border border-border-subtle bg-surface px-3 py-2 text-xs text-text-primary ' +
  'placeholder:text-text-tertiary focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary ' +
  'disabled:opacity-50 disabled:cursor-not-allowed';

const SECTION_TITLE = 'mb-2 text-[10px] font-bold uppercase text-text-tertiary';

export function TicketPanel({ conversationId, crmContactId }: TicketPanelProps) {
  const tenantId = getTenantId();
  const [ticket, setTicket] = useState<Ticket | null>(null);
  const [loading, setLoading] = useState(false);
  const [newSubject, setNewSubject] = useState('');
  const [error, setError] = useState<string | null>(null);

  // PRODUCT.6-B: no real ERP ticketing connector is configured for any
  // tenant today (internal/tool/connectors.CRMConnector has no wired
  // implementation in production/pilot runtime — see the containment
  // human gate). Checked proactively on mount via GET .../ticket so the
  // panel never shows the create form only to fail on submit.
  const [ticketingUnavailable, setTicketingUnavailable] = useState<boolean | null>(null);

  useEffect(() => {
    let active = true;
    const checkTicketing = async () => {
      try {
        await axios.get(`${API_BASE}/tenants/${tenantId}/conversations/${conversationId}/ticket`, { headers: authHeaders() });
        if (active) setTicketingUnavailable(false);
      } catch (err: any) {
        if (!active) return;
        // 503 = no real connector configured (PRODUCT.6-B). Any other
        // status (e.g. 404 "no ticket for this conversation" once a real
        // connector exists) means the integration itself is available.
        setTicketingUnavailable(err.response?.status === 503);
      }
    };
    void checkTicketing();
    return () => {
      active = false;
    };
  }, [tenantId, conversationId]);

  // R5.2: CRM Activity
  const [companies, setCompanies] = useState<Company[]>([]);
  const [selectedCompanyId, setSelectedCompanyId] = useState('');
  const [activitySubject, setActivitySubject] = useState('');
  const [loadingCompanies, setLoadingCompanies] = useState(false);

  useEffect(() => {
    // Load companies on mount (from K3G CRM via ListCompanies endpoint)
    const loadCompanies = async () => {
      setLoadingCompanies(true);
      try {
        const res = await axios.get(`${API_BASE}/integrations/companies`, { headers: authHeaders() });
        setCompanies(res.data.items || []);
        if (res.data.items && res.data.items.length > 0) {
          setSelectedCompanyId(res.data.items[0].id);
        }
      } catch (err: any) {
        console.error('Erro ao carregar empresas:', err);
        // Not critical: the panel shows "Nenhuma empresa disponível".
      } finally {
        setLoadingCompanies(false);
      }
    };
    loadCompanies();
  }, []);

  const createTicket = async () => {
    if (!newSubject.trim()) return;

    setLoading(true);
    setError(null);
    try {
      const res = await axios.post(
        `${API_BASE}/tenants/${tenantId}/conversations/${conversationId}/ticket`,
        { subject: newSubject },
        { headers: authHeaders() }
      );
      setTicket(res.data);
      setNewSubject('');
    } catch (err: any) {
      setError(err.response?.data?.message || 'Erro ao criar ticket');
    } finally {
      setLoading(false);
    }
  };

  const updateTicketStatus = async (status: string) => {
    if (!ticket) return;

    setLoading(true);
    setError(null);
    try {
      const res = await axios.patch(
        `${API_BASE}/tenants/${tenantId}/conversations/${conversationId}/ticket/${ticket.id}`,
        { status },
        { headers: authHeaders() }
      );
      setTicket(res.data);
    } catch (err: any) {
      setError(err.response?.data?.message || 'Erro ao atualizar ticket');
    } finally {
      setLoading(false);
    }
  };

  const closeTicket = async () => {
    if (!ticket) return;

    setLoading(true);
    setError(null);
    try {
      await axios.post(
        `${API_BASE}/tenants/${tenantId}/conversations/${conversationId}/ticket/${ticket.id}/close`,
        {},
        { headers: authHeaders() }
      );
      setTicket({ ...ticket, status: 'closed' });
    } catch (err: any) {
      setError(err.response?.data?.message || 'Erro ao fechar ticket');
    } finally {
      setLoading(false);
    }
  };

  // R5.2: Create activity in CRM
  const createActivity = async () => {
    if (!activitySubject.trim() || !selectedCompanyId || !crmContactId) return;

    setLoading(true);
    setError(null);
    try {
      await axios.post(
        `${API_BASE}/tenants/${tenantId}/conversations/${conversationId}/crm/activity`,
        {
          subject: activitySubject,
          company_id: selectedCompanyId,
          contact_id: crmContactId,
        },
        { headers: authHeaders() }
      );
      setActivitySubject('');
    } catch (err: any) {
      setError(err.response?.data?.message || 'Erro ao criar atividade no CRM');
    } finally {
      setLoading(false);
    }
  };

  const noCompanies = !loadingCompanies && companies.length === 0;

  return (
    <div className="border-b border-border-subtle px-4">
      {error && (
        <div
          role="alert"
          className="mt-3 rounded-control border border-status-danger-border bg-status-danger-soft px-3 py-2 text-xs text-status-danger"
        >
          {error}
        </div>
      )}

      <section className="py-3" aria-labelledby="ticket-panel-heading">
        <h3 id="ticket-panel-heading" className={SECTION_TITLE}>Chamado</h3>

        {ticketingUnavailable === null ? null : ticketingUnavailable ? (
          <div className="rounded-control border border-border-subtle bg-surface-muted p-3 text-xs text-text-secondary">
            <p className="font-semibold text-text-primary">Chamados no ERP não configurados</p>
            <p className="mt-1">Abrir, atualizar e fechar chamados requer a integração de ERP do tenant, que ainda não está configurada.</p>
          </div>
        ) : ticket ? (
          <div className="rounded-control border border-border-subtle bg-surface p-3">
            <div className="flex min-w-0 items-start justify-between gap-2">
              <div className="min-w-0">
                <code className="block font-mono text-[10px] text-text-tertiary">{ticket.id.slice(0, 8)}</code>
                <p className="mt-1 text-[9px] font-semibold uppercase text-text-tertiary">Assunto</p>
                <p className="mt-0.5 truncate text-xs font-semibold text-text-primary" title={ticket.subject}>
                  {ticket.subject}
                </p>
              </div>
              <span
                className={clsx(
                  'shrink-0 rounded-control border px-1.5 py-0.5 text-[9px] font-bold',
                  STATUS_CLASS[ticket.status] ?? STATUS_CLASS.closed
                )}
              >
                {ticket.status}
              </span>
            </div>

            {ticket.status !== 'closed' && (
              <div className="mt-3 flex flex-wrap gap-1.5">
                {ticket.status !== 'in_progress' && (
                  <Button type="button" variant="secondary" size="sm" disabled={loading} onClick={() => updateTicketStatus('in_progress')}>
                    Trabalhando
                  </Button>
                )}
                {ticket.status !== 'resolved' && (
                  <Button type="button" variant="secondary" size="sm" disabled={loading} onClick={() => updateTicketStatus('resolved')}>
                    Resolvido
                  </Button>
                )}
                <Button
                  type="button"
                  variant="secondary"
                  size="sm"
                  className="!border-status-danger-border !bg-status-danger-soft !text-status-danger"
                  disabled={loading}
                  onClick={closeTicket}
                >
                  Fechar
                </Button>
              </div>
            )}
          </div>
        ) : (
          <form
            className="flex min-w-0 gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              void createTicket();
            }}
          >
            <input
              className={FIELD}
              value={newSubject}
              onChange={(e) => setNewSubject(e.target.value)}
              placeholder="Novo chamado..."
              aria-label="Novo chamado"
              disabled={loading}
            />
            <Button type="submit" size="sm" disabled={loading || !newSubject.trim()}>
              Abrir
            </Button>
          </form>
        )}
      </section>

      {crmContactId && (
        <section className="border-t border-border-subtle py-3" aria-labelledby="crm-activity-heading">
          <h3 id="crm-activity-heading" className={SECTION_TITLE}>Atividade CRM</h3>
          <form
            className="grid gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              void createActivity();
            }}
          >
            <select
              className={FIELD}
              aria-label="Empresa"
              value={selectedCompanyId}
              onChange={(e) => setSelectedCompanyId(e.target.value)}
              disabled={loading || loadingCompanies || companies.length === 0}
            >
              {companies.length === 0 && <option value="">Selecionar empresa</option>}
              {companies.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                  {c.cnpj ? ` (${c.cnpj})` : ''}
                </option>
              ))}
            </select>
            {noCompanies && <p className="m-0 text-[10px] text-text-tertiary">Nenhuma empresa disponível</p>}
            <input
              className={FIELD}
              value={activitySubject}
              onChange={(e) => setActivitySubject(e.target.value)}
              placeholder="Descrição da atividade..."
              aria-label="Descrição da atividade"
              disabled={loading}
            />
            <Button type="submit" size="sm" disabled={loading || !activitySubject.trim() || !selectedCompanyId}>
              {loading ? 'Criando...' : 'Criar Atividade'}
            </Button>
          </form>
        </section>
      )}
    </div>
  );
}
