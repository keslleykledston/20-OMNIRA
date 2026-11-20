import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Button, ConfirmDialog } from '../../primitives'
import { useDebounced } from '../../../hooks/useDebounced'
import {
  groupAiAPI,
  groupAiErrorMessage,
  type CreateMemoryItemRequest,
  type MemoryItem,
  type MemoryKind,
  type PatchMemoryItemRequest,
  type ProposalKind,
} from '../../../lib/groupAi'
import { KINDS, KIND_LABEL, analysisPending, consolidationPending, filterMemory, formatWhen, lastRunText, type StatusFilter } from '../../../lib/groupAiModel'
import { showToast } from '../../../lib/toast'
import { groupAiKey, useGroupMemory } from '../../../lib/useGroupAi'
import { getTenantId } from '../../../lib/session'
import { MemoryItemCard } from './MemoryItemCard'
import { Notice, SectionTitle, SelectField, fieldClass } from './ui'

interface Props {
  groupId: string
  canManage: boolean
  proposalsAvailable: boolean
  onOpenSettings: () => void
  onOpenProposals: () => void
}

const STATUS_OPTIONS: { value: StatusFilter; label: string }[] = [
  { value: 'active', label: 'Abertos e resolvidos' },
  { value: 'open', label: 'Só abertos' },
  { value: 'resolved', label: 'Só resolvidos' },
  { value: 'superseded', label: 'Substituídos' },
]

// The memory of the group: what the AI (or a person) recorded, with its evidence. The AI never acts here; a person confirms, edits,
// pins, resolves or deletes. Search asks the server (it also reinforces what is found); the kind and status filters only narrow what is loaded.
export function MemoryTab({ groupId, canManage, proposalsAvailable, onOpenSettings, onOpenProposals }: Props) {
  const qc = useQueryClient()
  const [kind, setKind] = useState<MemoryKind | ''>('')
  const [status, setStatus] = useState<StatusFilter>('active')
  const [search, setSearch] = useState('')
  const q = useDebounced(search.trim(), 300)
  const [error, setError] = useState<string | null>(null)
  const [adding, setAdding] = useState(false)
  const [deleting, setDeleting] = useState<MemoryItem | null>(null)

  const memory = useGroupMemory(groupId, { enabled: true, includeSuperseded: status === 'superseded', poll: true })
  const found = useQuery({
    queryKey: [...groupAiKey(groupId), 'search', q],
    queryFn: () => groupAiAPI.searchMemory(groupId, q),
    enabled: q.length > 0 && !!getTenantId(),
    retry: false,
  })

  const aiEnabled = !!memory.data?.group.ai_enabled
  const state = memory.data?.state
  const searching = q.length > 0
  const source = searching ? found.data?.items : memory.data?.items
  const items = filterMemory(source ?? [], { kind, status })

  const refresh = () => void qc.invalidateQueries({ queryKey: groupAiKey(groupId) })
  const fail = (action: Parameters<typeof groupAiErrorMessage>[1]) => (err: unknown) => {
    setError(groupAiErrorMessage(err, action))
    refresh() // a stale screen reloads what the server has
  }

  const patch = useMutation({
    mutationFn: ({ item, body }: { item: MemoryItem; body: PatchMemoryItemRequest }) => groupAiAPI.patchItem(groupId, item.id, { ...body, version: item.version }),
    onSuccess: () => { setError(null); refresh() },
    onError: fail('patch_item'),
  })
  const remove = useMutation({
    mutationFn: (item: MemoryItem) => groupAiAPI.deleteItem(groupId, item.id),
    onSuccess: () => { setError(null); setDeleting(null); refresh() },
    onError: (err) => { setDeleting(null); fail('delete_item')(err) },
  })
  const draft = useMutation({
    mutationFn: ({ item, kind: k }: { item: MemoryItem; kind: ProposalKind }) => groupAiAPI.draftProposal(groupId, item.id, k),
    onSuccess: (r) => {
      setError(null)
      refresh()
      showToast(
        r.existing
          ? 'Este item já tinha uma proposta. Ela está em Propostas.'
          : 'Proposta criada em Propostas. Nada foi criado ainda: uma pessoa precisa aprovar.',
        'info',
      )
      onOpenProposals()
    },
    onError: fail('draft'),
  })
  const analyze = useMutation({
    mutationFn: () => groupAiAPI.analyze(groupId),
    onSuccess: () => { setError(null); refresh(); showToast('Pedido enviado. A leitura acontece em segundo plano e pode levar alguns minutos.', 'info') },
    onError: fail('analyze'),
  })
  const consolidate = useMutation({
    mutationFn: () => groupAiAPI.consolidate(groupId),
    onSuccess: () => { setError(null); refresh(); showToast('Pedido enviado. A condensação acontece em segundo plano.', 'info') },
    onError: fail('consolidate'),
  })
  const create = useMutation({
    mutationFn: (body: CreateMemoryItemRequest) => groupAiAPI.createItem(groupId, body),
    onSuccess: () => { setError(null); setAdding(false); refresh(); showToast('Item registrado.') },
    onError: fail('create_item'),
  })

  const busy = patch.isPending || remove.isPending || draft.isPending
  const analyzeWaiting = analysisPending(state)
  const consolidateWaiting = consolidationPending(state)

  const saveText = async (item: MemoryItem, summary: string) => {
    try {
      await patch.mutateAsync({ item, body: { summary } })
      return true
    } catch {
      return false // onError already explained and reloaded
    }
  }

  return (
    <div className="space-y-4 p-4">
      {memory.isLoading && <p className="py-6 text-center text-sm text-text-secondary">Carregando a memória...</p>}
      {memory.isError && (
        <p role="alert" className="text-sm text-status-danger">
          {groupAiErrorMessage(memory.error, 'load')}
        </p>
      )}

      {memory.data && (
        <>
          {!aiEnabled && (
            <Notice tone="warning">
              A IA está desligada neste grupo: ela não lê mensagens novas e nada é enviado ao provedor de IA. A memória abaixo continua disponível para consulta.
              {canManage && (
                <>
                  {' '}
                  <button type="button" onClick={onOpenSettings} className="font-medium text-accent-primary underline">
                    Ligar a IA
                  </button>
                </>
              )}
            </Notice>
          )}

          <section aria-label="Leitura da IA" className="space-y-2 rounded-card border border-border-subtle bg-surface-muted p-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <SectionTitle>Leitura da IA</SectionTitle>
              {canManage && (
                <div className="flex gap-2">
                  <Button size="sm" variant="secondary" disabled={!aiEnabled || analyzeWaiting} isLoading={analyze.isPending} onClick={() => analyze.mutate()}>
                    Analisar agora
                  </Button>
                  <Button size="sm" variant="secondary" disabled={!aiEnabled || consolidateWaiting} isLoading={consolidate.isPending} onClick={() => consolidate.mutate()}>
                    Consolidar agora
                  </Button>
                </div>
              )}
            </div>
            <p className="text-xs text-text-secondary">{lastRunText(state)}</p>
            {analyzeWaiting && (
              <p role="status" className="text-xs text-text-secondary">
                Pedido de leitura enviado em {formatWhen(state?.analyze_requested_at)}. Quem lê é um processo em segundo plano: a lista se atualiza sozinha quando terminar.
              </p>
            )}
            {consolidateWaiting && (
              <p role="status" className="text-xs text-text-secondary">
                Pedido de condensação enviado em {formatWhen(state?.consolidate_requested_at)}. Itens fixados, confirmados ou ainda abertos não são condensados.
              </p>
            )}
            <p className="text-xs text-text-tertiary">
              {state?.last_consolidated_at ? `Última condensação em ${formatWhen(state.last_consolidated_at)}. ` : ''}
              {state?.runs ? `${state.runs} ${state.runs === 1 ? 'leitura feita' : 'leituras feitas'} até agora.` : ''}
            </p>
          </section>

          {error && <Notice tone="danger" role="alert">{error}</Notice>}

          <div className="space-y-2">
            <input
              type="search"
              aria-label="Buscar na memória"
              placeholder="Perguntar à memória: o que sabemos sobre...?"
              value={search}
              maxLength={200}
              onChange={(e) => setSearch(e.target.value)}
              className={fieldClass}
            />
            <div className="flex flex-wrap items-center gap-2">
              <SelectField label="Filtrar por tipo" value={kind} onChange={(e) => setKind(e.target.value as MemoryKind | '')}>
                <option value="">Todos os tipos</option>
                {KINDS.map((k) => (
                  <option key={k} value={k}>
                    {KIND_LABEL[k]}
                  </option>
                ))}
              </SelectField>
              <SelectField label="Filtrar por situação" value={status} onChange={(e) => setStatus(e.target.value as StatusFilter)}>
                {STATUS_OPTIONS.map((o) => (
                  <option key={o.value} value={o.value}>
                    {o.label}
                  </option>
                ))}
              </SelectField>
              {canManage && (
                <Button size="sm" variant={adding ? 'secondary' : 'primary'} className="ml-auto" disabled={!aiEnabled && !adding} onClick={() => setAdding((v) => !v)}>
                  {adding ? 'Fechar' : 'Registrar item'}
                </Button>
              )}
            </div>
            {canManage && !aiEnabled && <p className="text-xs text-text-tertiary">Para registrar itens à mão, ligue a IA do grupo em Ajustes.</p>}
          </div>

          {adding && <AddItemForm pending={create.isPending} onSubmit={(b) => create.mutate(b)} />}

          <section aria-label="Itens da memória" className="space-y-2">
            <div className="flex items-baseline justify-between">
              <SectionTitle>{searching ? 'Resultado da busca' : 'Memória do grupo'}</SectionTitle>
              <span className="text-xs text-text-tertiary">{searching && found.isFetching ? 'Buscando...' : `${items.length} ${items.length === 1 ? 'item' : 'itens'}`}</span>
            </div>
            {searching && found.isError && (
              <p role="alert" className="text-xs text-status-danger">
                {groupAiErrorMessage(found.error, 'search')}
              </p>
            )}
            {items.length === 0 && !(searching && (found.isFetching || found.isError)) ? (
              <p className="rounded-card border border-dashed border-border-subtle px-3 py-6 text-center text-sm text-text-secondary">
                {searching
                  ? 'A memória não tem nada sobre isso.'
                  : 'A memória deste grupo está vazia. Ela se enche quando a IA lê o grupo, quando um arquivo é importado ou quando uma pessoa registra um item.'}
              </p>
            ) : (
              <ul className="space-y-2">
                {items.map((item) => (
                  <MemoryItemCard
                    key={item.id}
                    item={item}
                    canManage={canManage}
                    proposalsAvailable={proposalsAvailable}
                    busy={busy}
                    onConfirm={(i) => patch.mutate({ item: i, body: { confirm: true } })}
                    onPin={(i) => patch.mutate({ item: i, body: { pinned: !i.pinned } })}
                    onToggleResolved={(i) => patch.mutate({ item: i, body: { status: i.status === 'resolved' ? 'open' : 'resolved' } })}
                    onDelete={setDeleting}
                    onDraft={(i, k) => draft.mutate({ item: i, kind: k })}
                    onSaveText={saveText}
                  />
                ))}
              </ul>
            )}
          </section>
        </>
      )}

      <ConfirmDialog
        open={!!deleting}
        title="Apagar este item da memória?"
        message="O item sai da memória do grupo. As mensagens do grupo não são afetadas. A IA pode voltar a registrá-lo se ele reaparecer numa leitura futura."
        confirmLabel="Apagar item"
        destructive
        isPending={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting)}
        onCancel={() => setDeleting(null)}
      />
    </div>
  )
}

function AddItemForm({ pending, onSubmit }: { pending: boolean; onSubmit: (b: CreateMemoryItemRequest) => void }) {
  const [kind, setKind] = useState<MemoryKind>('fact')
  const [summary, setSummary] = useState('')
  const [actor, setActor] = useState('')
  const [status, setStatus] = useState<'open' | 'resolved'>('open')
  const valid = summary.trim().length > 0

  return (
    <form
      aria-label="Registrar item na memória"
      className="space-y-2 rounded-card border border-border-subtle bg-surface p-3"
      onSubmit={(e) => {
        e.preventDefault()
        if (!valid) return
        onSubmit({
          kind,
          summary: summary.trim(),
          ...(actor.trim() ? { actor_label: actor.trim() } : {}),
          ...(kind === 'request' ? { status } : {}),
        })
      }}
    >
      <p className="text-xs text-text-secondary">O item fica confirmado por pessoa: a IA não o sobrescreve. Não escreva senhas, chaves nem tokens.</p>
      <div className="flex flex-wrap gap-2">
        <SelectField label="Tipo do item" value={kind} onChange={(e) => setKind(e.target.value as MemoryKind)}>
          {KINDS.map((k) => (
            <option key={k} value={k}>
              {KIND_LABEL[k]}
            </option>
          ))}
        </SelectField>
        {kind === 'request' && (
          <SelectField label="Situação do pedido" value={status} onChange={(e) => setStatus(e.target.value as 'open' | 'resolved')}>
            <option value="open">Aberto</option>
            <option value="resolved">Resolvido</option>
          </SelectField>
        )}
      </div>
      <textarea aria-label="Resumo do item" placeholder="O que é importante saber sobre o grupo?" rows={3} maxLength={600} value={summary} onChange={(e) => setSummary(e.target.value)} className={fieldClass} />
      <input aria-label="Quem (opcional)" placeholder="Quem (nome ou papel, opcional; nunca telefone)" maxLength={120} value={actor} onChange={(e) => setActor(e.target.value)} className={fieldClass} />
      <div className="flex items-center justify-between">
        <span className="text-xs text-text-tertiary">{summary.length}/600</span>
        <Button type="submit" size="sm" disabled={!valid} isLoading={pending}>
          Registrar
        </Button>
      </div>
    </form>
  )
}
