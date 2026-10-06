import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Badge, Button, ConfirmDialog, ErrorState, LoadingState, PermissionState, Tabs, TextArea, Modal } from '../components/primitives'
import FlowCanvas, { type PendingConnection } from '../components/flows/FlowCanvas'
import NodePalette from '../components/flows/NodePalette'
import NodeInspector from '../components/flows/NodeInspector'
import IssuesPanel from '../components/flows/IssuesPanel'
import SimulatorPanel from '../components/flows/SimulatorPanel'
import VersionsPanel from '../components/flows/VersionsPanel'
import FlowSettingsModal from '../components/flows/FlowSettingsModal'
import { flowErrorBody, flowErrorMessage, flowErrorStatus, flowsAPI, type FlowDefinition, type Issue, type Scenario, type SimulationResult } from '../lib/flows'
import { addNode, autoLayout, connect, disconnect, hasBlocking, moveNode, removeNode, stableJSON, updateConfig } from '../lib/flowModel'
import { queuesAPI } from '../lib/queues'
import { useChannelLines } from '../lib/channelLines'
import { getTenantId } from '../lib/session'
import { useAccess } from '../lib/useAccess'
import { useUnsavedGuard } from '../lib/useUnsavedGuard'

type SideTab = 'props' | 'issues' | 'simulate' | 'versions'

// Editor visual (ADR-0019). Edita só o RASCUNHO; publicar cria uma versão imutável. A UI nunca executa nada: valida e simula
// pelo backend, que é a autoridade. A validação ao vivo aparece enquanto se edita e o botão Publicar só liga com o rascunho
// salvo, sem erros bloqueantes e com permissão flow.publish.
export default function FlowEditorPage() {
  const { flowId = '' } = useParams()
  const tenantId = getTenantId()
  const access = useAccess()
  const qc = useQueryClient()
  const canView = access.can('flow.view')
  const canEdit = access.can('flow.edit')
  const canTest = access.can('flow.test')
  const canPublish = access.can('flow.publish')
  const canArchive = access.can('flow.archive')

  const flowQ = useQuery({ queryKey: ['flow', tenantId, flowId], queryFn: () => flowsAPI.get(flowId), enabled: canView && !!flowId, retry: false })
  const typesQ = useQuery({ queryKey: ['flow-node-types', tenantId], queryFn: flowsAPI.nodeTypes, enabled: canView, staleTime: 300_000, retry: false })
  const queuesQ = useQuery({ queryKey: ['queues', tenantId], queryFn: queuesAPI.list, enabled: canView && canEdit, retry: false })
  const subflowsQ = useQuery({ queryKey: ['flows-subflows', tenantId], queryFn: () => flowsAPI.list({ type: 'SUBFLOW', status: 'published' }), enabled: canView && canEdit, retry: false })
  const versionsQ = useQuery({ queryKey: ['flow-versions', tenantId, flowId], queryFn: () => flowsAPI.versions(flowId), enabled: canView && !!flowId, retry: false })
  const lines = useChannelLines()

  const [def, setDef] = useState<FlowDefinition | null>(null)
  const [name, setName] = useState('')
  const [revision, setRevision] = useState(0)
  const [savedJSON, setSavedJSON] = useState('')
  const [issues, setIssues] = useState<Issue[]>([])
  const [checking, setChecking] = useState(false)
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [pending, setPending] = useState<PendingConnection | null>(null)
  const [tab, setTab] = useState<SideTab>('props')
  const [notice, setNotice] = useState<{ tone: 'info' | 'error'; text: string } | null>(null)
  const [conflict, setConflict] = useState(false)
  const [sim, setSim] = useState<SimulationResult | null>(null)
  const [simError, setSimError] = useState<string | null>(null)
  const [publishing, setPublishing] = useState(false)
  const [publishNote, setPublishNote] = useState('')
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [archiveOpen, setArchiveOpen] = useState(false)
  const loadedFor = useRef('')
  const [reloadTick, setReloadTick] = useState(0) // "Recarregar" precisa reaplicar o servidor mesmo quando a busca devolve dados idênticos
  const local = useRef<{ def: FlowDefinition | null; savedJSON: string }>({ def: null, savedJSON: '' })
  local.current = { def, savedJSON }

  // Carrega o rascunho uma vez por revisão do servidor. Nunca sobrescreve edição em andamento: se outra pessoa salvou uma
  // revisão mais nova enquanto há alterações locais não salvas, mantém as locais e pede uma recarga explícita (Recarregar).
  useEffect(() => {
    const f = flowQ.data
    if (!f) return
    const key = `${f.id}:${f.draft_revision}`
    if (loadedFor.current === key) return
    const hasUnsaved = !!local.current.def && stableJSON(local.current.def) !== local.current.savedJSON
    if (loadedFor.current.startsWith(`${f.id}:`) && hasUnsaved) {
      loadedFor.current = key
      setConflict(true)
      setNotice({ tone: 'error', text: 'Outra pessoa salvou uma versão mais nova deste rascunho. Suas alterações não salvas foram mantidas; recarregue para ver a versão do servidor (isso descarta as suas).' })
      return
    }
    loadedFor.current = key
    setDef(f.definition)
    setName(f.name)
    setRevision(f.draft_revision)
    setSavedJSON(stableJSON(f.definition))
    setConflict(false)
  }, [flowQ.data, reloadTick])

  const dirty = !!def && (stableJSON(def) !== savedJSON || name !== (flowQ.data?.name ?? name))
  const archived = flowQ.data?.status === 'archived'
  const readOnly = !canEdit || archived

  // Validação ao vivo (debounce): o backend julga; a UI só mostra.
  useEffect(() => {
    if (!def || !canView) return undefined
    setChecking(true)
    const t = window.setTimeout(() => {
      const run = canEdit ? flowsAPI.validate(def) : flowsAPI.validateStored(flowId)
      run.then((r) => setIssues(r.issues)).catch(() => undefined).finally(() => setChecking(false))
    }, 500)
    return () => window.clearTimeout(t)
  }, [def, canView, canEdit, flowId])

  useUnsavedGuard(dirty)

  const edit = useCallback((fn: (d: FlowDefinition) => FlowDefinition) => setDef((d) => (d ? fn(d) : d)), [])

  const save = useMutation({
    mutationFn: () => flowsAPI.saveDraft(flowId, { revision, name: name.trim() || (flowQ.data?.name ?? ''), description: flowQ.data?.description, definition: def as FlowDefinition }),
    onSuccess: (r) => {
      setRevision(r.flow.draft_revision)
      setSavedJSON(stableJSON(def as FlowDefinition))
      setIssues(r.issues)
      loadedFor.current = `${r.flow.id}:${r.flow.draft_revision}`
      setNotice({ tone: 'info', text: 'Rascunho salvo.' })
      qc.invalidateQueries({ queryKey: ['flow', tenantId, flowId] })
      qc.invalidateQueries({ queryKey: ['flows', tenantId] })
    },
    onError: (err) => {
      if (flowErrorBody(err).error === 'revision_conflict') setConflict(true)
      setNotice({ tone: 'error', text: flowErrorMessage(err) })
    },
  })

  const publish = useMutation({
    mutationFn: () => flowsAPI.publish(flowId, { revision, note: publishNote.trim() || undefined }),
    onSuccess: (r) => {
      setPublishing(false)
      setPublishNote('')
      setNotice({ tone: 'info', text: `Versão ${r.version.version} publicada. Ela vale para as próximas conversas.${r.warnings.length ? ` (${r.warnings.length} aviso(s))` : ''}` })
      qc.invalidateQueries({ queryKey: ['flow', tenantId, flowId] })
      qc.invalidateQueries({ queryKey: ['flow-versions', tenantId, flowId] })
      qc.invalidateQueries({ queryKey: ['flows', tenantId] })
    },
    onError: (err) => {
      setPublishing(false)
      const body = flowErrorBody(err)
      if (body.issues) setIssues(body.issues)
      if (body.error === 'revision_conflict') setConflict(true)
      setTab('issues')
      setNotice({ tone: 'error', text: flowErrorMessage(err) })
    },
  })

  const activate = useMutation({
    mutationFn: (version: number) => flowsAPI.activate(flowId, version),
    onSuccess: () => {
      setNotice({ tone: 'info', text: 'Versão reativada. Conversas em andamento continuam na versão em que começaram.' })
      qc.invalidateQueries({ queryKey: ['flow', tenantId, flowId] })
      qc.invalidateQueries({ queryKey: ['flow-versions', tenantId, flowId] })
    },
    onError: (err) => setNotice({ tone: 'error', text: flowErrorMessage(err) }),
  })

  const settings = useMutation({
    mutationFn: (v: { priority: number; is_default: boolean; restart_policy: 'new_conversation_only' | 'always'; connection_ids: string[]; providers: string[] }) =>
      flowsAPI.updateSettings(flowId, { priority: v.priority, is_default: v.is_default, restart_policy: v.restart_policy, trigger_filter: { connection_ids: v.connection_ids, providers: v.providers } }),
    onSuccess: () => {
      setSettingsOpen(false)
      // As configurações não fazem parte do rascunho (não mudam draft_revision): não recarregar a definição aqui, senão as
      // edições locais não salvas seriam sobrescritas.
      setNotice({ tone: 'info', text: 'Configurações salvas.' })
      qc.invalidateQueries({ queryKey: ['flow', tenantId, flowId] })
      qc.invalidateQueries({ queryKey: ['flows', tenantId] })
    },
  })

  const archive = useMutation({
    mutationFn: () => flowsAPI.archive(flowId),
    onSuccess: () => {
      setArchiveOpen(false)
      qc.invalidateQueries({ queryKey: ['flow', tenantId, flowId] })
      qc.invalidateQueries({ queryKey: ['flows', tenantId] })
    },
    onError: (err) => { setArchiveOpen(false); setNotice({ tone: 'error', text: flowErrorMessage(err) }) },
  })

  const simulate = useMutation({
    mutationFn: (scenario: Scenario) => flowsAPI.simulate(flowId, { definition: def as FlowDefinition, scenario }),
    onSuccess: (r) => { setSim(r); setSimError(null) },
    onError: (err) => { setSim(null); setSimError(flowErrorMessage(err)) },
  })

  const selected = def?.nodes.find((n) => n.id === selectedId) ?? null
  const hasTrigger = !!def?.nodes.some((n) => n.type === 'trigger')
  const hasAI = !!def?.nodes.some((n) => n.type.startsWith('ai_'))
  const blocking = hasBlocking(issues)
  const variables = useMemo(() => {
    const names = new Set<string>()
    def?.variables.forEach((v) => names.add(v.name))
    def?.nodes.forEach((n) => {
      const c = (n.config ?? {}) as Record<string, unknown>
      if (typeof c.variable === 'string' && c.variable) names.add(c.variable)
      if (Array.isArray(c.assignments)) (c.assignments as { variable?: string }[]).forEach((a) => a.variable && names.add(a.variable))
    })
    return [...names].sort()
  }, [def])
  const highlight = sim?.steps.map((s) => s.node_id)

  const onFinishConnect = (target: string) => {
    if (!pending || !def) return
    const r = connect(def, pending.source, pending.port, target)
    setPending(null)
    if (r.ok) setDef(r.def)
    else setNotice({ tone: 'error', text: r.reason })
  }

  if (!access.isLoading && !canView) {
    return <div className="px-6 py-6 lg:px-8"><PermissionState message="Você não tem permissão para ver os fluxos de atendimento." /></div>
  }
  if (flowQ.isLoading || !def) {
    if (flowQ.isError) {
      return <div className="px-6 py-6 lg:px-8"><ErrorState message={flowErrorStatus(flowQ.error) === 404 ? 'Fluxo não encontrado.' : flowErrorMessage(flowQ.error)} /></div>
    }
    return <div className="px-6 py-6 lg:px-8"><LoadingState message="Carregando o fluxo…" /></div>
  }
  const flow = flowQ.data!

  return (
    <div className="flex h-[calc(100vh-theme(spacing.16))] flex-col">
      <header className="flex flex-wrap items-center justify-between gap-3 border-b border-border-subtle bg-surface px-4 py-3">
        <div className="flex min-w-0 items-center gap-3">
          <Link to="/flows" className="text-sm text-accent-primary no-underline">← Fluxos</Link>
          <input
            aria-label="Nome do fluxo"
            className="min-w-0 rounded-control border border-transparent bg-transparent px-2 py-1 text-base font-semibold text-text-primary hover:border-border-subtle focus:border-accent-primary"
            value={name}
            disabled={readOnly}
            onChange={(e) => setName(e.target.value)}
          />
          <Badge variant={flow.status === 'published' ? 'success' : flow.status === 'archived' ? 'default' : 'warning'}>
            {flow.status === 'published' ? `Publicado (v${flow.active_version})` : flow.status === 'archived' ? 'Arquivado' : 'Rascunho'}
          </Badge>
          {dirty && <span className="text-xs text-status-warning-strong" role="status">Alterações não salvas</span>}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {canEdit && !archived && <Button size="sm" variant="secondary" onClick={() => edit(autoLayout)}>Organizar</Button>}
          {canEdit && !archived && <Button size="sm" variant="secondary" onClick={() => setSettingsOpen(true)}>Configurações</Button>}
          {canEdit && !archived && <Button size="sm" variant="secondary" isLoading={save.isPending} disabled={!dirty || conflict || save.isPending} onClick={() => save.mutate()}>Salvar rascunho</Button>}
          {canPublish && !archived && (
            <Button size="sm" disabled={dirty || blocking || checking || conflict || publish.isPending} title={dirty ? 'Salve o rascunho antes de publicar' : blocking ? 'Corrija os erros antes de publicar' : undefined} onClick={() => setPublishing(true)}>
              Publicar
            </Button>
          )}
          {canArchive && !archived && <Button size="sm" variant="tertiary" onClick={() => setArchiveOpen(true)}>Arquivar</Button>}
        </div>
      </header>

      {notice && (
        <div role={notice.tone === 'error' ? 'alert' : 'status'} className={`flex items-center justify-between px-4 py-2 text-sm ${notice.tone === 'error' ? 'bg-status-danger-soft text-status-danger' : 'bg-status-info-soft text-text-primary'}`}>
          <span>{notice.text}</span>
          <span className="flex gap-2">
            {conflict && <Button size="sm" variant="secondary" onClick={() => { loadedFor.current = ''; setConflict(false); setNotice(null); setReloadTick((t) => t + 1); flowQ.refetch() }}>Recarregar</Button>}
            <button type="button" aria-label="Dispensar aviso" className="text-text-tertiary" onClick={() => setNotice(null)}>×</button>
          </span>
        </div>
      )}
      {archived && <p className="bg-surface-muted px-4 py-2 text-sm text-text-secondary">Este fluxo está arquivado: somente leitura.</p>}
      {!canEdit && !archived && <p className="bg-surface-muted px-4 py-2 text-sm text-text-secondary">Você pode ver este fluxo{canTest ? ' e simulá-lo' : ''}, mas não editá-lo.</p>}
      {pending && <p role="status" className="bg-accent-primary-soft px-4 py-1.5 text-sm text-text-primary">Escolha o nó de destino (clique no círculo de entrada dele) ou clique no vazio para cancelar.</p>}

      <div className="grid min-h-0 flex-1 grid-cols-1 lg:grid-cols-[220px_1fr_400px]">
        <aside className="overflow-y-auto border-r border-border-subtle bg-surface p-3">
          {readOnly ? <p className="text-xs text-text-tertiary">Paleta indisponível em modo leitura.</p> : (
            <NodePalette
              types={typesQ.data ?? []}
              hasTrigger={hasTrigger}
              onAdd={(type) => {
                const r = addNode(def, type, undefined, selectedId ?? undefined)
                setDef(r.def)
                setSelectedId(r.id)
                setTab('props')
              }}
            />
          )}
        </aside>
        <main className="min-h-[360px] min-w-0">
          <FlowCanvas
            def={def}
            selectedId={selectedId}
            issues={issues}
            pending={pending}
            readOnly={readOnly}
            highlight={highlight}
            onSelect={(id) => { setSelectedId(id); if (id) setTab('props') }}
            onMove={(id, pos) => edit((d) => moveNode(d, id, pos))}
            onStartConnect={setPending}
            onFinishConnect={onFinishConnect}
            onDisconnect={(edgeId) => edit((d) => disconnect(d, edgeId))}
          />
        </main>
        <aside className="flex min-h-0 flex-col overflow-hidden border-l border-border-subtle bg-surface">
          <div className="overflow-x-auto border-b border-border-subtle px-3 pt-2">
            <Tabs<SideTab>
              aria-label="Painel lateral do editor"
              items={[
                { id: 'props', label: 'Detalhes' },
                { id: 'issues', label: `Problemas${issues.length ? ` (${issues.length})` : ''}` },
                { id: 'simulate', label: 'Simular', disabled: !canTest },
                { id: 'versions', label: 'Versões' },
              ]}
              value={tab}
              onChange={setTab}
            />
          </div>
          <div className="flex-1 overflow-y-auto p-3">
            {tab === 'props' && (selected ? (
              <NodeInspector
                node={selected}
                queues={(queuesQ.data ?? []).map((q) => ({ id: q.id, name: q.name }))}
                subflows={(subflowsQ.data ?? []).filter((f) => f.id !== flowId)}
                variables={variables}
                readOnly={readOnly}
                onConfig={(cfg) => edit((d) => updateConfig(d, selected.id, cfg))}
                onDelete={() => { edit((d) => removeNode(d, selected.id)); setSelectedId(null) }}
              />
            ) : (
              <p className="text-sm text-text-secondary">Selecione um nó no canvas para editar suas propriedades. Para ligar dois nós, clique no círculo de uma saída e depois no círculo de entrada do destino.</p>
            ))}
            {tab === 'issues' && <IssuesPanel issues={issues} checking={checking} onSelectNode={(id) => { setSelectedId(id); setTab('props') }} />}
            {tab === 'simulate' && <SimulatorPanel disabled={!canTest} running={simulate.isPending} result={sim} error={simError} hasAI={hasAI} onRun={(s) => simulate.mutate(s)} />}
            {tab === 'versions' && <VersionsPanel flow={flow} versions={versionsQ.data ?? []} canPublish={canPublish && !archived} busy={activate.isPending} onActivate={(v) => activate.mutate(v)} />}
          </div>
        </aside>
      </div>

      <Modal
        open={publishing}
        title="Publicar fluxo"
        description="Será criada uma nova versão, que nunca mais muda. Ela passa a valer para as próximas conversas; as em andamento continuam na versão atual."
        onClose={() => setPublishing(false)}
        footer={<><Button variant="secondary" onClick={() => setPublishing(false)}>Cancelar</Button><Button isLoading={publish.isPending} onClick={() => publish.mutate()}>Publicar</Button></>}
      >
        <TextArea label="Nota da versão (opcional)" value={publishNote} maxLength={500} rows={3} onChange={(e) => setPublishNote(e.target.value)} />
      </Modal>
      <FlowSettingsModal open={settingsOpen} flow={flow} lines={lines.data ?? []} saving={settings.isPending} error={settings.isError ? flowErrorMessage(settings.error) : null} onClose={() => setSettingsOpen(false)} onSave={(v) => settings.mutate(v)} />
      <ConfirmDialog open={archiveOpen} title="Arquivar este fluxo?" message="Um fluxo arquivado deixa de atender conversas novas e não pode mais ser editado." confirmLabel="Arquivar" destructive isPending={archive.isPending} onConfirm={() => archive.mutate()} onCancel={() => setArchiveOpen(false)} />
    </div>
  )
}
