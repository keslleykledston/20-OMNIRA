import React, { useState } from 'react';
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

interface TicketPanelProps {
  conversationId: string;
}

export function TicketPanel({ conversationId }: TicketPanelProps) {
  const tenantId = getTenantId();
  const [ticket, setTicket] = useState<Ticket | null>(null);
  const [loading, setLoading] = useState(false);
  const [newSubject, setNewSubject] = useState('');
  const [error, setError] = useState<string | null>(null);

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

  return (
    <div style={{ padding: '12px', borderTop: '1px solid #e0e0e0', backgroundColor: '#fafafa' }}>
      <h3 style={{ margin: '0 0 12px 0', fontSize: '14px', fontWeight: 600 }}>Chamado</h3>

      {error && (
        <div style={{ padding: '8px', backgroundColor: '#ffebee', color: '#c62828', borderRadius: '4px', marginBottom: '8px', fontSize: '12px' }}>
          {error}
        </div>
      )}

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
  );
}
