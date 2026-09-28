import React, { useEffect, useState } from 'react';
import axios from 'axios';
import clsx from 'clsx';
import { API_BASE } from '../../lib/config';
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from '../../lib/session';
import { Icon } from '../primitives';

interface ConversationSummaryProps {
  conversationId: string;
}

// PRODUCT.7C1: human-triggered only — never auto-generated on open/change.
// Nothing here is persisted (no localStorage/IndexedDB); switching
// conversations must reset to idle, never show the previous conversation's
// summary as if it belonged to the new one.
type Phase = 'idle' | 'loading' | 'success' | 'unavailable' | 'rate_limited';

export default function ConversationSummary({ conversationId }: ConversationSummaryProps) {
  const [phase, setPhase] = useState<Phase>('idle');
  const [summary, setSummary] = useState('');
  const [errorMessage, setErrorMessage] = useState('');

  useEffect(() => {
    setPhase('idle');
    setSummary('');
    setErrorMessage('');
  }, [conversationId]);

  const handleGenerate = async () => {
    setPhase('loading');
    setErrorMessage('');
    try {
      const tenantId = getTenantId();
      const res = await axios.post(
        `${API_BASE}/tenants/${tenantId}/conversations/${conversationId}/ai/summary`,
        {},
        { headers: authHeaders() }
      );
      setSummary(typeof res.data?.summary === 'string' ? res.data.summary : '');
      setPhase('success');
    } catch (err: any) {
      if (isUnauthorized(err)) {
        handleUnauthorized();
        return;
      }
      if (err?.response?.status === 429) {
        setPhase('rate_limited');
        return;
      }
      setErrorMessage(describeSummaryError(err?.response?.status));
      setPhase('unavailable');
    }
  };

  return (
    <div className="p-4 border-b border-border-subtle">
      <h4 className="text-xs font-semibold text-text-tertiary mb-3 uppercase">Resumo com IA</h4>

      {phase === 'success' ? (
        <div className="space-y-2">
          <p className="text-sm text-text-primary whitespace-pre-wrap break-words">{summary}</p>
          <button
            onClick={handleGenerate}
            className="text-xs font-medium text-accent-primary hover:underline"
          >
            Gerar novamente
          </button>
        </div>
      ) : (
        <>
          <button
            onClick={handleGenerate}
            disabled={phase === 'loading'}
            className={clsx(
              'w-full px-3 py-2 text-sm font-medium rounded-control transition-colors flex items-center justify-center',
              'bg-surface text-text-primary hover:bg-surface-muted',
              'border border-border-subtle',
              phase === 'loading' && 'opacity-50 cursor-not-allowed'
            )}
          >
            <Icon name="sparkles" className="w-4 h-4 mr-2" />
            {phase === 'loading' ? 'Gerando...' : 'Gerar resumo'}
          </button>
          {phase === 'rate_limited' && (
            <p role="alert" className="mt-2 text-xs text-status-warning">
              Muitas solicitações de resumo. Tente novamente em instantes.
            </p>
          )}
          {phase === 'unavailable' && (
            <p role="alert" className="mt-2 text-xs text-status-danger">
              {errorMessage}
            </p>
          )}
        </>
      )}
    </div>
  );
}

function describeSummaryError(status?: number): string {
  if (status === 503) return 'Resumo por IA não está disponível no momento.';
  if (status === 404) return 'Conversa não encontrada.';
  if (status === 422) return 'Não há conteúdo suficiente nesta conversa para gerar um resumo.';
  if (status === 502 || status === 504) return 'O provedor de IA não respondeu. Tente novamente em instantes.';
  return 'Não foi possível gerar o resumo agora.';
}
