import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { attendanceAPI, attendanceErrorMessage, type HistoryHit } from '../../lib/attendance'

const when = (iso: string) => new Date(iso).toLocaleDateString('pt-BR', { day: '2-digit', month: '2-digit', year: 'numeric' })

// Busca por trecho nas mensagens ANTERIORES do mesmo contato (ADR-0020): "o que ele disse da fatura?". Só dispara quando a pessoa
// pede; nada é carregado ao abrir a conversa. O contato é o da conversa: não há como buscar outro.
export default function HistorySearch({ conversationId }: { conversationId: string }) {
  const [q, setQ] = useState('')
  const [hits, setHits] = useState<HistoryHit[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const search = useMutation({
    mutationFn: (text: string) => attendanceAPI.searchHistory(conversationId, text),
    onSuccess: (items) => { setHits(items); setError(null) },
    onError: (err) => { setHits(null); setError(attendanceErrorMessage(err)) },
  })
  const text = q.trim()
  const submit = () => { if (text.length >= 2 && !search.isPending) search.mutate(text) }

  return (
    <section aria-label="Buscar no histórico" className="border-b border-border-subtle p-4">
      <h4 className="mb-2 text-xs font-semibold uppercase text-text-tertiary">Histórico do contato</h4>
      <form className="flex gap-2" onSubmit={(e) => { e.preventDefault(); submit() }}>
        <input
          aria-label="Buscar no histórico do contato"
          className="min-w-0 flex-1 rounded-control border border-border-subtle bg-surface px-3 py-1.5 text-sm text-text-primary"
          placeholder="Ex.: fatura, instalação…"
          maxLength={100}
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />
        <button type="submit" disabled={text.length < 2 || search.isPending} className="rounded-control border border-border-subtle bg-surface px-3 py-1.5 text-sm font-medium text-text-primary hover:bg-surface-muted disabled:opacity-50">
          Buscar
        </button>
      </form>
      {error && <p role="alert" className="mt-2 rounded-control bg-status-danger-soft p-2 text-xs text-status-danger">{error}</p>}
      {hits && hits.length === 0 && <p className="mt-2 text-xs text-text-tertiary">Nada encontrado nas conversas anteriores deste contato.</p>}
      {hits && hits.length > 0 && (
        <ul className="m-0 mt-2 list-none space-y-2 p-0" aria-label="Resultados da busca">
          {hits.map((h, i) => (
            <li key={`${h.conversation_id}-${h.at}-${i}`} className="rounded-control border border-border-subtle bg-surface p-2 text-xs">
              <div className="flex items-center justify-between gap-2 text-text-tertiary">
                <span className="font-semibold text-text-secondary">{h.role === 'customer' ? 'Cliente' : 'Atendente'}</span>
                <span>{when(h.at)}</span>
              </div>
              <p className="m-0 mt-1 text-text-primary">{h.snippet}</p>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
