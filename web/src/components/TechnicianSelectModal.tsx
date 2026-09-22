import React, { useState, useEffect } from 'react';
import axios from 'axios';
import { API_BASE } from '../lib/config';
import { authHeaders, getTenantId } from '../lib/session';

interface Technician {
  id: string;
  email: string;
  name: string;
}

interface TechnicianSelectModalProps {
  isOpen: boolean;
  title: string;
  onSelect: (technicianId: string) => Promise<void>;
  onClose: () => void;
  excludeUserIds?: string[];
}

export function TechnicianSelectModal({
  isOpen,
  title,
  onSelect,
  onClose,
  excludeUserIds = [],
}: TechnicianSelectModalProps) {
  const tenantId = getTenantId();
  const [technicians, setTechnicians] = useState<Technician[]>([]);
  const [loading, setLoading] = useState(false);
  const [selecting, setSelecting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!isOpen) return;

    const loadTechnicians = async () => {
      setLoading(true);
      setError(null);
      try {
        const res = await axios.get(
          `${API_BASE}/tenants/${tenantId}/agents`,
          { headers: authHeaders() }
        );
        const agents = (res.data.items || []).map((a: any) => ({
          id: a.user_id,
          email: a.email,
          name: a.name || a.email,
        }));
        setTechnicians(agents.filter((t: Technician) => !excludeUserIds.includes(t.id)));
      } catch (err: any) {
        setError(err.response?.data?.message || 'Erro ao carregar técnicos');
        // Fallback para mock only in development (never in production/staging)
        if (import.meta.env.DEV) {
          setTechnicians([
            { id: '22222222-2222-2222-2222-222222222222', email: 'alice@omnira.local', name: 'Alice' },
            { id: '33333333-3333-3333-3333-333333333333', email: 'bob@omnira.local', name: 'Bob' },
          ].filter((t) => !excludeUserIds.includes(t.id)));
        } else {
          setTechnicians([]);
        }
      } finally {
        setLoading(false);
      }
    };

    loadTechnicians();
  }, [isOpen, excludeUserIds]);

  const handleSelect = async (technicianId: string) => {
    setSelecting(true);
    setError(null);
    try {
      await onSelect(technicianId);
      onClose();
    } catch (err: any) {
      setError(err.response?.data?.message || 'Erro ao executar ação');
    } finally {
      setSelecting(false);
    }
  };

  if (!isOpen) return null;

  return (
    <div style={{
      position: 'fixed',
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
      backgroundColor: 'rgba(0, 0, 0, 0.5)',
      display: 'flex',
      justifyContent: 'center',
      alignItems: 'center',
      zIndex: 1000,
    }}>
      <div style={{
        backgroundColor: 'white',
        borderRadius: '8px',
        padding: '24px',
        maxWidth: '400px',
        width: '90%',
        maxHeight: '80vh',
        overflow: 'auto',
        boxShadow: '0 4px 12px rgba(0, 0, 0, 0.15)',
      }}>
        <h2 style={{ margin: '0 0 16px 0', fontSize: '18px', fontWeight: 600 }}>{title}</h2>

        {error && (
          <div style={{
            padding: '12px',
            backgroundColor: '#ffebee',
            color: '#c62828',
            borderRadius: '4px',
            marginBottom: '16px',
            fontSize: '13px',
          }}>
            {error}
          </div>
        )}

        {loading ? (
          <div style={{ padding: '16px', textAlign: 'center', color: '#999' }}>Carregando técnicos...</div>
        ) : technicians.length === 0 ? (
          <div style={{ padding: '16px', textAlign: 'center', color: '#999' }}>Nenhum técnico disponível</div>
        ) : (
          <div style={{ display: 'flex', flexDirection: 'column', gap: '8px' }}>
            {technicians.map((tech) => (
              <button
                key={tech.id}
                onClick={() => handleSelect(tech.id)}
                disabled={selecting}
                style={{
                  padding: '12px 16px',
                  textAlign: 'left',
                  border: '1px solid #ddd',
                  borderRadius: '4px',
                  backgroundColor: 'white',
                  cursor: selecting ? 'default' : 'pointer',
                  opacity: selecting ? 0.6 : 1,
                  transition: 'background-color 0.2s',
                }}
                onMouseEnter={(e) => {
                  if (!selecting) {
                    e.currentTarget.style.backgroundColor = '#f5f5f5';
                  }
                }}
                onMouseLeave={(e) => {
                  e.currentTarget.style.backgroundColor = 'white';
                }}
              >
                <div style={{ fontSize: '14px', fontWeight: 500 }}>{tech.name}</div>
                <div style={{ fontSize: '12px', color: '#666', marginTop: '4px' }}>{tech.email}</div>
              </button>
            ))}
          </div>
        )}

        <div style={{ marginTop: '24px', display: 'flex', gap: '8px', justifyContent: 'flex-end' }}>
          <button
            onClick={onClose}
            disabled={selecting}
            style={{
              padding: '8px 16px',
              fontSize: '13px',
              border: '1px solid #ddd',
              borderRadius: '4px',
              backgroundColor: 'white',
              cursor: selecting ? 'default' : 'pointer',
              opacity: selecting ? 0.6 : 1,
            }}
          >
            Cancelar
          </button>
        </div>
      </div>
    </div>
  );
}
