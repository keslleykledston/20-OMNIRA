import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Badge, Button, LoadingState, Modal } from '../primitives'
import { flowErrorMessage, flowsAPI, type InstallResult, type PackInfo, type TemplateInfo } from '../../lib/flows'
import type { Queue } from '../../lib/queues'
import { getTenantId } from '../../lib/session'

type Step = 'choose' | 'map' | 'confirm' | 'done'

interface Props {
  pack?: PackInfo
  template?: TemplateInfo
  queues: Queue[]
  onClose: () => void
}

// Instala um pack (ou um modelo) como RASCUNHOS do tenant. Nada é publicado e nada tem vínculo com o modelo depois.
// O assistente só reúne as escolhas; o backend recusa mapeamento ausente, desconhecido ou de outro tenant antes de gravar.
export default function InstallWizard({ pack, template, queues, onClose }: Props) {
  const tenantId = getTenantId()
  const [step, setStep] = useState<Step>(pack ? 'choose' : 'map')
  const [selected, setSelected] = useState<string[]>(() => (pack ? pack.items.filter((i) => !i.optional && !i.dependency).map((i) => i.template) : []))
  const [mappings, setMappings] = useState<Record<string, string>>({})
  const [result, setResult] = useState<InstallResult | null>(null)

  const preview = useQuery({
    queryKey: ['flow-pack-preview', tenantId, pack?.slug, [...selected].sort().join(',')],
    queryFn: () => flowsAPI.pack(pack!.slug, selected),
    enabled: !!pack && selected.length > 0,
    retry: false,
    placeholderData: (prev) => prev,
  })
  const info = pack ? preview.data ?? pack : null
  const needed = pack ? info?.mappings ?? [] : template?.mappings ?? []

  const missing = needed.filter((m) => m.required && !mappings[m.key])
  const install = useMutation({
    mutationFn: () => (pack ? flowsAPI.installPack(pack.slug, { templates: selected, mappings }) : flowsAPI.installTemplate(template!.slug, mappings)),
    onSuccess: (r) => { setResult(r); setStep('done') },
  })

  // Se só há uma fila, ela já é a escolha óbvia para todos os campos (a pessoa ainda vê e pode trocar).
  useEffect(() => {
    if (queues.length !== 1 || needed.length === 0) return
    setMappings((cur) => {
      const next = { ...cur }
      for (const m of needed) if (!next[m.key]) next[m.key] = queues[0].id
      return next
    })
  }, [queues, needed])

  const title = pack ? `Instalar ${pack.name}` : `Instalar ${template?.name ?? ''}`
  const queueName = useMemo(() => new Map(queues.map((q) => [q.id, q.name])), [queues])
  const toggle = (slug: string) => setSelected((s) => (s.includes(slug) ? s.filter((x) => x !== slug) : [...s, slug]))

  return (
    <Modal
      open
      title={title}
      description={step === 'done' ? undefined : 'Os fluxos são criados como rascunhos seus. Nada é publicado até você revisar, simular e publicar.'}
      onClose={onClose}
      footer={
        step === 'done' ? (
          <Button onClick={onClose}>Concluir</Button>
        ) : (
          <>
            <Button variant="secondary" onClick={onClose}>Cancelar</Button>
            {step === 'map' && pack && <Button variant="secondary" onClick={() => setStep('choose')}>Voltar</Button>}
            {step === 'confirm' && <Button variant="secondary" onClick={() => setStep('map')}>Voltar</Button>}
            {step === 'choose' && <Button disabled={selected.length === 0 || preview.isFetching} onClick={() => setStep('map')}>Continuar</Button>}
            {step === 'map' && <Button disabled={missing.length > 0} title={missing.length ? `Falta mapear: ${missing.map((m) => m.key).join(', ')}` : undefined} onClick={() => setStep('confirm')}>Continuar</Button>}
            {step === 'confirm' && <Button isLoading={install.isPending} disabled={install.isPending} onClick={() => install.mutate()}>Instalar</Button>}
          </>
        )
      }
    >
      <div className="space-y-4">
        {step === 'choose' && pack && (
          <fieldset className="space-y-2">
            <legend className="text-sm font-semibold text-text-primary">O que instalar</legend>
            {pack.items.filter((i) => !i.dependency).map((i) => (
              <label key={i.template} className="flex items-start gap-2 text-sm text-text-primary">
                <input type="checkbox" checked={selected.includes(i.template)} onChange={() => toggle(i.template)} className="mt-1" />
                <span>{i.name}{i.optional && <span className="text-text-tertiary"> (opcional)</span>}</span>
              </label>
            ))}
            {info && info.items.some((i) => i.dependency) && (
              <p className="text-xs text-text-secondary">Também serão instalados, porque os escolhidos dependem deles: {info.items.filter((i) => i.dependency).map((i) => i.name).join(', ')}.</p>
            )}
            {preview.isFetching && <p className="text-xs text-text-tertiary">Atualizando…</p>}
          </fieldset>
        )}

        {step === 'map' && (
          <div className="space-y-3">
            <h3 className="text-sm font-semibold text-text-primary">Para onde cada atendimento vai</h3>
            {needed.length === 0 && <p className="text-sm text-text-secondary">Nenhuma fila precisa ser escolhida.</p>}
            {queues.length === 0 && needed.length > 0 && (
              <p role="alert" className="rounded-control bg-status-warning-soft p-2 text-sm text-text-primary">Você ainda não tem filas. Crie as filas de atendimento antes de instalar.</p>
            )}
            {needed.map((m) => (
              <label key={m.key} className="block">
                <span className="mb-1 block text-xs font-medium text-text-secondary">{m.description}</span>
                <select aria-label={m.description} className="w-full rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm" value={mappings[m.key] ?? ''} onChange={(e) => setMappings({ ...mappings, [m.key]: e.target.value })}>
                  <option value="">Selecione uma fila</option>
                  {queues.map((q) => <option key={q.id} value={q.id}>{q.name}</option>)}
                </select>
              </label>
            ))}
          </div>
        )}

        {step === 'confirm' && (
          <div className="space-y-3">
            <h3 className="text-sm font-semibold text-text-primary">Confirme</h3>
            <ul className="m-0 list-disc space-y-1 pl-5 text-sm text-text-primary">
              {(pack ? (info?.items ?? []).filter((i) => selected.includes(i.template) || i.dependency) : [{ template: template!.slug, name: template!.name, dependency: false }]).map((i) => (
                <li key={i.template}>{i.name}{i.dependency && <span className="text-text-tertiary"> (dependência)</span>}</li>
              ))}
            </ul>
            <dl className="space-y-1 text-sm">
              {needed.map((m) => (
                <div key={m.key} className="flex justify-between gap-3"><dt className="text-text-secondary">{m.description}</dt><dd className="font-medium text-text-primary">{queueName.get(mappings[m.key]) ?? '—'}</dd></div>
              ))}
            </dl>
            <p className="text-xs text-text-secondary">Se o identificador de um fluxo já existir, o novo recebe um sufixo numérico e o seu não é alterado.</p>
            {install.isError && <p role="alert" className="text-sm text-status-danger">{flowErrorMessage(install.error)}</p>}
          </div>
        )}

        {step === 'done' && result && (
          <div className="space-y-3" data-testid="install-result">
            <p className="text-sm font-semibold text-text-primary">Pronto: {result.flows.length} fluxo(s) criado(s) como rascunho.</p>
            <ul className="m-0 list-none space-y-1 p-0">
              {result.flows.map((f) => (
                <li key={f.flow_id} className="flex items-center justify-between gap-2 text-sm">
                  <span>{f.slug}{f.is_default && <Badge size="sm" variant="info" className="ml-2">padrão</Badge>}{f.dependency && <span className="ml-2 text-xs text-text-tertiary">dependência</span>}</span>
                  <Link to={`/flows/${f.flow_id}`} className="text-accent-primary">Abrir</Link>
                </li>
              ))}
            </ul>
            {result.notes?.map((n) => <p key={n} className="text-xs text-text-secondary">{n}</p>)}
            <p className="text-xs text-text-secondary">Próximo passo: abra cada fluxo, ajuste os textos, use o simulador e publique primeiro os subfluxos e depois o fluxo de entrada.</p>
          </div>
        )}
        {preview.isLoading && step === 'choose' && <LoadingState message="Carregando…" />}
      </div>
    </Modal>
  )
}
