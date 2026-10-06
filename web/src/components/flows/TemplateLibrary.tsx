import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Badge, Button, Card, CardBody, EmptyState, ErrorState, LoadingState, SearchField, Modal } from '../primitives'
import InstallWizard from './InstallWizard'
import { flowErrorMessage, flowsAPI, type PackInfo, type TemplateInfo } from '../../lib/flows'
import { queuesAPI } from '../../lib/queues'
import { getTenantId } from '../../lib/session'
import { nodeLabel } from '../../lib/flowModel'

const CATEGORY_LABEL: Record<string, string> = {
  GENERAL: 'Geral', CUSTOMER_SERVICE: 'Atendimento', AFTER_HOURS: 'Fora do horário', SURVEY: 'Pesquisa', K3G: 'K3G', TECHNICAL_SUPPORT: 'Suporte técnico', NETWORK: 'Redes',
  ISP: 'Provedor (ISP)', NOC: 'NOC', FINANCIAL: 'Financeiro', COMMERCIAL: 'Comercial',
}

const PROFILE_LABEL: Record<string, string> = { general: 'Atendimento geral', isp: 'Provedor de internet', msp: 'Empresa de TI / MSP' }

interface Props {
  canInstall: boolean
}

export default function TemplateLibrary({ canInstall }: Props) {
  const tenantId = getTenantId()
  const [profile, setProfile] = useState('')
  const [q, setQ] = useState('')
  const [category, setCategory] = useState('')
  const [wizard, setWizard] = useState<{ pack?: PackInfo; template?: TemplateInfo } | null>(null)
  const [preview, setPreview] = useState<TemplateInfo | null>(null)

  const packs = useQuery({ queryKey: ['flow-packs', tenantId, profile], queryFn: () => flowsAPI.packs(profile || undefined), retry: false })
  const templates = useQuery({ queryKey: ['flow-templates', tenantId, category, q, profile], queryFn: () => flowsAPI.templates({ category: category || undefined, q: q || undefined, recommended_for: profile || undefined }), retry: false })
  const queues = useQuery({ queryKey: ['queues', tenantId], queryFn: queuesAPI.list, enabled: canInstall, retry: false })

  const categories = useMemo(() => [...new Set((templates.data ?? []).flatMap((t) => t.categories))].sort(), [templates.data])

  const openPreview = async (slug: string) => setPreview(await flowsAPI.template(slug))

  if (packs.isError && templates.isError) return <ErrorState message={flowErrorMessage(packs.error)} />
  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-end gap-3">
        <label className="block">
          <span className="mb-1 block text-xs font-medium text-text-secondary">Meu perfil</span>
          <select aria-label="Perfil da operação" className="rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm" value={profile} onChange={(e) => setProfile(e.target.value)}>
            <option value="">Todos</option>
            {Object.entries(PROFILE_LABEL).map(([k, l]) => <option key={k} value={k}>{l}</option>)}
          </select>
        </label>
      </div>

      <section aria-label="Packs de modelos" className="space-y-3">
        <h2 className="text-base font-semibold text-text-primary">Packs</h2>
        {packs.isLoading && <LoadingState message="Carregando packs…" />}
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          {(packs.data ?? []).map((p) => (
            <Card key={p.slug}>
              <CardBody>
                <div className="flex items-start justify-between gap-2">
                  <div>
                    <h3 className="text-sm font-semibold text-text-primary">{p.name}</h3>
                    <p className="mt-1 text-sm text-text-secondary">{p.description}</p>
                  </div>
                  <Badge size="sm">v{p.version}</Badge>
                </div>
                <p className="mt-2 text-xs text-text-tertiary">{p.items.filter((i) => !i.dependency).length} modelos · pede {p.mappings.length} fila(s)</p>
                {p.optional_features.length > 0 && <p className="text-xs text-text-tertiary">Melhora com: {p.optional_features.join(', ')}</p>}
                <div className="mt-3">
                  {canInstall ? <Button size="sm" onClick={() => setWizard({ pack: p })}>Instalar pack</Button> : <span className="text-xs text-text-tertiary">Você não tem permissão para instalar.</span>}
                </div>
              </CardBody>
            </Card>
          ))}
        </div>
      </section>

      <section aria-label="Modelos" className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h2 className="text-base font-semibold text-text-primary">Modelos</h2>
          <div className="flex flex-wrap items-center gap-2">
            <SearchField placeholder="Buscar modelo…" value={q} onChange={(e) => setQ(e.target.value)} onClear={() => setQ('')} aria-label="Buscar modelo" />
            <select aria-label="Categoria" className="rounded-control border border-border-subtle bg-surface px-3 py-2 text-sm" value={category} onChange={(e) => setCategory(e.target.value)}>
              <option value="">Todas as categorias</option>
              {categories.map((c) => <option key={c} value={c}>{CATEGORY_LABEL[c] ?? c}</option>)}
            </select>
          </div>
        </div>
        {templates.isLoading && <LoadingState message="Carregando modelos…" />}
        {!templates.isLoading && (templates.data ?? []).length === 0 && <EmptyState title="Nenhum modelo encontrado" description="Ajuste a busca ou o filtro." />}
        <ul className="m-0 grid list-none grid-cols-1 gap-3 p-0 md:grid-cols-2 xl:grid-cols-3">
          {(templates.data ?? []).map((t) => (
            <li key={t.slug}>
              <Card className="h-full">
                <CardBody>
                  <div className="flex items-start justify-between gap-2">
                    <h3 className="text-sm font-semibold text-text-primary">{t.name}</h3>
                    <Badge size="sm" variant={t.difficulty === 'starter' ? 'success' : 'info'}>{t.difficulty === 'starter' ? 'inicial' : 'intermediário'}</Badge>
                  </div>
                  <p className="mt-1 text-sm text-text-secondary">{t.description}</p>
                  <p className="mt-2 text-xs text-text-tertiary">{t.categories.map((c) => CATEGORY_LABEL[c] ?? c).join(' · ')}</p>
                  <div className="mt-3 flex gap-2">
                    <Button size="sm" variant="secondary" onClick={() => openPreview(t.slug)}>Ver detalhes</Button>
                    {canInstall && <Button size="sm" onClick={() => setWizard({ template: t })}>Instalar</Button>}
                  </div>
                </CardBody>
              </Card>
            </li>
          ))}
        </ul>
      </section>

      {wizard && <InstallWizard pack={wizard.pack} template={wizard.template} queues={queues.data ?? []} onClose={() => setWizard(null)} />}

      <Modal open={!!preview} title={preview?.name ?? ''} description={preview?.description} onClose={() => setPreview(null)} footer={<Button onClick={() => setPreview(null)}>Fechar</Button>}>
        {preview && (
          <div className="space-y-3 text-sm">
            <p className="text-text-secondary">Versão {preview.version} · {preview.test_cases} cenário(s) de teste já validados.</p>
            {preview.mappings.length > 0 && (
              <div>
                <h3 className="font-semibold text-text-primary">Filas que ele pede</h3>
                <ul className="m-0 list-disc pl-5 text-text-secondary">{preview.mappings.map((m) => <li key={m.key}>{m.description}</li>)}</ul>
              </div>
            )}
            {preview.requires_subflows.length > 0 && <p className="text-text-secondary">Usa os subfluxos: {preview.requires_subflows.join(', ')}.</p>}
            <p className="text-text-secondary">Recursos necessários: {preview.required_features.join(', ')}{preview.optional_features.length ? ` · opcionais: ${preview.optional_features.join(', ')}` : ''}.</p>
            {preview.definition && (
              <div>
                <h3 className="font-semibold text-text-primary">Passos ({preview.definition.nodes.length})</h3>
                <p className="text-text-secondary">{[...new Set(preview.definition.nodes.map((n) => nodeLabel(n.type)))].join(', ')}.</p>
              </div>
            )}
          </div>
        )}
      </Modal>
    </div>
  )
}
