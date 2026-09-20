import React, { useState, useEffect } from 'react';
import axios from 'axios';
import { API_BASE } from '../lib/config';
import { authHeaders, getTenantId } from '../lib/session';

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

export function TicketPanel({ conversationId, crmContactId }: TicketPanelProps) {
  const tenantId = getTenantId();
  const [ticket, setTicket] = useState<Ticket | null>(null);
  const [loading, setLoading] = useState(false);
  const [newSubject, setNewSubject] = useState('');
  const [error, setError] = useState<string | null>(null);

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
        const res = await axios.get(
          `${API_BASE}/integrations/companies`,
          { headers: authHeaders() }
        );
        setCompanies(res.data.items || []);
        if (res.data.items && res.data.items.length > 0) {
          setSelectedCompanyId(res.data.items[0].id);
        }
      } catch (err: any) {
        console.error('Erro ao carregar empresas:', err);
        // Silently fail — not critical if companies don't load
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
      // Clear form on success
      setActivitySubject('');
      // Show success message
      setError(null); // Clear any previous error
    } catch (err: any) {
      setError(err.response?.data?.message || 'Erro ao criar atividade no CRM');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div style={{ padding: '12px', borderTop: '1px solid #e0e0e0', backgroundColor: '#fafafa' }}>
      {error && (
        <div style={{ padding: '8px', backgroundColor: '#ffebee', color: '#c62828', borderRadius: '4px', marginBottom: '8px', fontSize: '12px' }}>
          {error}
        </div>
      )}

      <div style={{ marginBottom: '16px' }}>
        <h3 style={{ margin: '0 0 12px 0', fontSize: '14px', fontWeight: 600 }}>Chamado</h3>

      {ticket ? (
        <div style={{ backgroundColor: 'white', padding: '8px', borderRadius: '4px', border: '1px solid #e0e0e0' }}>
          <div style={{ marginBottom: '8px' }}>
            <span style={{ fontSize: '12px', color: '#666' }}>ID: </span>
            <span style={{ fontSize: '12px', fontFamily: 'monospace' }}>{ticket.id.slice(0, 8)}</span>
          </div>
          <div style={{ marginBottom: '8px' }}>
            <span style={{ fontSize: '12px', color: '#666' }}>Assunto: </span>
            <span style={{ fontSize: '12px' }}>{ticket.subject}</span>
          </div>
          <div style={{ marginBottom: '8px' }}>
            <span style={{ fontSize: '12px', color: '#666' }}>Status: </span>
            <span style={{ fontSize: '12px', fontWeight: 500 }}>{ticket.status}</span>
          </div>

          <div style={{ display: 'flex', gap: '6px', marginTop: '8px', flexWrap: 'wrap' }}>
            {ticket.status !== 'in_progress' && (
              <button
                onClick={() => updateTicketStatus('in_progress')}
                disabled={loading}
                style={{
                  padding: '4px 8px',
                  fontSize: '12px',
                  backgroundColor: '#2196F3',
                  color: 'white',
                  border: 'none',
                  borderRadius: '4px',
                  cursor: loading ? 'default' : 'pointer',
                  opacity: loading ? 0.6 : 1,
                }}
              >
                Trabalhando
              </button>
            )}
            {ticket.status !== 'resolved' && (
              <button
                onClick={() => updateTicketStatus('resolved')}
                disabled={loading}
                style={{
                  padding: '4px 8px',
                  fontSize: '12px',
                  backgroundColor: '#4CAF50',
                  color: 'white',
                  border: 'none',
                  borderRadius: '4px',
                  cursor: loading ? 'default' : 'pointer',
                  opacity: loading ? 0.6 : 1,
                }}
              >
                Resolvido
              </button>
            )}
            {ticket.status !== 'closed' && (
              <button
                onClick={closeTicket}
                disabled={loading}
                style={{
                  padding: '4px 8px',
                  fontSize: '12px',
                  backgroundColor: '#f44336',
                  color: 'white',
                  border: 'none',
                  borderRadius: '4px',
                  cursor: loading ? 'default' : 'pointer',
                  opacity: loading ? 0.6 : 1,
                }}
              >
                Fechar
              </button>
            )}
          </div>
        </div>
      ) : (
        <div style={{ display: 'flex', gap: '6px' }}>
          <input
            type="text"
            placeholder="Novo chamado..."
            value={newSubject}
            onChange={(e) => setNewSubject(e.target.value)}
            style={{
              flex: 1,
              padding: '6px 8px',
              fontSize: '12px',
              border: '1px solid #ddd',
              borderRadius: '4px',
            }}
          />
          <button
            onClick={createTicket}
            disabled={loading || !newSubject.trim()}
            style={{
              padding: '6px 12px',
              fontSize: '12px',
              backgroundColor: '#2196F3',
              color: 'white',
              border: 'none',
              borderRadius: '4px',
              cursor: loading || !newSubject.trim() ? 'default' : 'pointer',
              opacity: loading || !newSubject.trim() ? 0.6 : 1,
            }}
          >
            Abrir
          </button>
        </div>
      )}
      </div>

      {crmContactId && (
        <div>
          <h3 style={{ margin: '0 0 12px 0', fontSize: '14px', fontWeight: 600 }}>Atividade CRM</h3>

          <div style={{ backgroundColor: 'white', padding: '8px', borderRadius: '4px', border: '1px solid #e0e0e0' }}>
            <div style={{ marginBottom: '8px' }}>
              <label style={{ fontSize: '12px', color: '#666', display: 'block', marginBottom: '4px' }}>
                Empresa
              </label>
              <select
                value={selectedCompanyId}
                onChange={(e) => setSelectedCompanyId(e.target.value)}
                disabled={loadingCompanies || companies.length === 0}
                style={{
                  width: '100%',
                  padding: '6px 8px',
                  fontSize: '12px',
                  border: '1px solid #ddd',
                  borderRadius: '4px',
                }}
              >
                {companies.map((company) => (
                  <option key={company.id} value={company.id}>
                    {company.name}
                  </option>
                ))}
              </select>
            </div>

            <div style={{ marginBottom: '8px' }}>
              <label style={{ fontSize: '12px', color: '#666', display: 'block', marginBottom: '4px' }}>
                Assunto
              </label>
              <input
                type="text"
                placeholder="Descrição da atividade..."
                value={activitySubject}
                onChange={(e) => setActivitySubject(e.target.value)}
                style={{
                  width: '100%',
                  padding: '6px 8px',
                  fontSize: '12px',
                  border: '1px solid #ddd',
                  borderRadius: '4px',
                  boxSizing: 'border-box',
                }}
              />
            </div>

            <button
              onClick={createActivity}
              disabled={loading || !activitySubject.trim() || !selectedCompanyId}
              style={{
                width: '100%',
                padding: '6px 12px',
                fontSize: '12px',
                backgroundColor: '#FF9800',
                color: 'white',
                border: 'none',
                borderRadius: '4px',
                cursor: loading || !activitySubject.trim() || !selectedCompanyId ? 'default' : 'pointer',
                opacity: loading || !activitySubject.trim() || !selectedCompanyId ? 0.6 : 1,
              }}
            >
              {loading ? 'Criando...' : 'Criar Atividade'}
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
