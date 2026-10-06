import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Badge, Button, EmptyState, ErrorState, Input, LoadingState, Table, TableBody, TableCell, TableHead, TableHeaderCell, TableRow, Modal } from '../primitives'
import { flowErrorMessage, flowsAPI, type Flow, type FlowType } from '../../lib/flows'
import { getTenantId } from '../../lib/session'

export const TYPE_LABEL: Record<FlowType, string> = {
  INBOUND: 'Atendimento (entrada)',
  INTERNAL: 'Interno',
  SUBFLOW: 'Subfluxo',
  EVENT: 'Por evento',
  SURVEY: 'Pesquisa',
  AFTER_HOURS: 'Fora do horário',
  AI_WORKFLOW: 'Com IA',
}

function slugify(name: string): string {
  return name.normalize('NFD').replace(/[̀-ͯ]/g, '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 60)
}

function StatusBadge({ flow }: { flow: Flow }) {
  if (flow.status === 'archived') return <Badge>Arquivado</Badge>
  if (flow.status === 'published') return <Badge variant="success">Publicado v{flow.active_version}</Badge>
  return <Badge variant="warning">Rascunho</Badge>
}

export default function FlowListSection({ canCreate }: { canCreate: boolean }) {
  const tenantId = getTenantId()
  const nav = useNavigate()
  const qc = useQueryClient()
  const flows = useQuery({ queryKey: ['flows', tenantId], queryFn: () => flowsAPI.list(), retry: false })
  const [creating, setCreating] = useState(false)
  const [name, setName] = useState('')
  const [slug, setSlug] = useState('')
  const [slugTouched, setSlugTouched] = useState(false)
  const [type, setType] = useState<FlowType>('INBOUND')

  const create = useMutation({
    mutationFn: () => flowsAPI.create({ slug, name: name.trim(), type }),
    onSuccess: (f) => {
      qc.invalidateQueries({ queryKey: ['flows', tenantId] })
      setCreating(false)
      nav(`/flows/${f.id}`)
    },
  })

  const items = flows.data ?? []
  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <p className="text-sm text-text-secondary">Fluxos conversam com o contato antes de uma pessoa assumir. Só a versão publicada atende conversas.</p>
        {canCreate && <Button onClick={() => { setName(''); setSlug(''); setSlugTouched(false); setType('INBOUND'); create.reset(); setCreating(true) }}>Novo fluxo</Button>}
      </div>
      {flows.isLoading && <LoadingState message="Carregando fluxos…" />}
      {flows.isError && <ErrorState message={flowErrorMessage(flows.error)} />}
      {!flows.isLoading && !flows.isError && items.length === 0 && (
        <EmptyState
          title="Nenhum fluxo ainda"
          description={canCreate ? 'Comece instalando um modelo pronto em "Modelos e packs" ou crie um fluxo do zero.' : 'Ainda não há fluxos neste ambiente.'}
        />
      )}
      {items.length > 0 && (
        <Table>
          <TableHead>
            <TableRow>
              <TableHeaderCell>Nome</TableHeaderCell>
              <TableHeaderCell>Tipo</TableHeaderCell>
              <TableHeaderCell>Situação</TableHeaderCell>
              <TableHeaderCell>Atualizado</TableHeaderCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {items.map((f) => (
              <TableRow key={f.id} onClick={() => nav(`/flows/${f.id}`)} className="cursor-pointer">
                <TableCell>
                  <button type="button" className="text-left font-medium text-accent-primary" onClick={(e) => { e.stopPropagation(); nav(`/flows/${f.id}`) }}>{f.name}</button>
                  {f.is_default && <Badge size="sm" variant="info" className="ml-2">padrão</Badge>}
                  {f.source_template_slug && <span className="ml-2 text-xs text-text-tertiary">de {f.source_template_slug}</span>}
                </TableCell>
                <TableCell>{TYPE_LABEL[f.type] ?? f.type}</TableCell>
                <TableCell><StatusBadge flow={f} /></TableCell>
                <TableCell>{new Date(f.updated_at).toLocaleString('pt-BR')}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      <Modal
        open={creating}
        title="Novo fluxo"
        description="O fluxo começa como rascunho vazio: nada atende conversas até você publicar."
        onClose={() => setCreating(false)}
        footer={<><Button variant="secondary" onClick={() => setCreating(false)}>Cancelar</Button><Button isLoading={create.isPending} disabled={!name.trim() || !slug || create.isPending} onClick={() => create.mutate()}>Criar</Button></>}
      >
        <div className="space-y-4">
          <Input label="Nome" value={name} onChange={(e) => { setName(e.target.value); if (!slugTouched) setSlug(slugify(e.target.value)) }} />
          <Input label="Identificador" value={slug} helperText="Letras minúsculas, números e hífen. Não muda depois." onChange={(e) => { setSlugTouched(true); setSlug(e.target.value) }} />
          <label className="block">
            <span className="mb-1 block text-xs font-medium text-text-secondary">Tipo</span>
            <select aria-label="Tipo do fluxo" className="w-full rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm" value={type} onChange={(e) => setType(e.target.value as FlowType)}>
              {(Object.keys(TYPE_LABEL) as FlowType[]).map((t) => <option key={t} value={t}>{TYPE_LABEL[t]}</option>)}
            </select>
          </label>
          {create.isError && <p role="alert" className="text-sm text-status-danger">{flowErrorMessage(create.error)}</p>}
        </div>
      </Modal>
    </div>
  )
}
