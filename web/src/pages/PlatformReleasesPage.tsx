import { useEffect, useRef, useState } from 'react'
import { useOutletContext } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Badge,
  Button,
  Card,
  CardBody,
  CardHeader,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  Input,
  LoadingState,
  Modal,
  PageHeader,
  PermissionState,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
  Tabs,
  TextArea,
  type TabItem,
} from '../components/primitives'
import type { usePlatformOperator } from '../hooks/usePlatformOperator'
import { handleUnauthorized, isUnauthorized } from '../lib/session'
import { showToast } from '../lib/toast'
import {
  describePlatformReleaseError,
  isTerminalRunState,
  platformInstallationAPI,
  platformPlansAPI,
  platformReleasesAPI,
  platformRunsAPI,
  runStateLabel,
  type PlatformRelease,
  type PlatformUpdatePlan,
  type PlatformUpdateRun,
} from '../lib/platformRelease'

// "Administração global → Plataforma → Versões e Atualizações" (ADR-0036: evolução aditiva do
// console existente, atrás da própria autorização do servidor — ver AdminShell/usePlatformOperator).
// Consome só o que internal/platformrelease/updatemgr (F6) de fato expõe em /api/v1/platform/*;
// nenhum dado aqui é inventado. O botão de iniciar atualização nunca aparece para quem o servidor
// recusaria (a tela toda está atrás do probe de operador), e mesmo assim cada mutação é
// revalidada pelo servidor a cada chamada — a tela não é a autoridade, só o reflexo dela.
//
// A especificação pede "acompanhamento SSE"; F6 implementou só polling por cursor (SSE documentado
// como pendência em IMPLEMENTATION-STATUS.md). Esta tela faz o acompanhamento por polling real
// (ver RunTracking abaixo) — a mesma adaptação, do mesmo jeito documentada aqui (F7).

type SectionId = 'catalogo' | 'atualizacoes'
const TAB_ITEMS: TabItem<SectionId>[] = [
  { id: 'catalogo', label: 'Catálogo' },
  { id: 'atualizacoes', label: 'Atualizações' },
]

export default function PlatformReleasesPage() {
  const qc = useQueryClient()
  // AdminShell já buscou isso para a própria guarda de navegação e repassa o MESMO resultado via
  // contexto do Outlet — NUNCA um segundo useQuery para esta chave aqui. Achado real em
  // navegador (nenhum teste jsdom reproduziu): uma segunda assinatura, montada só quando a
  // guarda libera o Outlet, disparava seu próprio refetchOnMount; enquanto em voo, a MESMA
  // guarda via isLoading=true de novo e desmontava o Outlet (e a assinatura nova com ele) —
  // ciclo de montar/desmontar para sempre.
  const { query: installation } = useOutletContext<ReturnType<typeof usePlatformOperator>>()
  const releases = useQuery({ queryKey: ['platform-releases'], queryFn: () => platformReleasesAPI.list(), enabled: installation.isSuccess, retry: false })
  const runs = useQuery({ queryKey: ['platform-runs'], queryFn: () => platformRunsAPI.list(), enabled: installation.isSuccess, retry: false, refetchInterval: 10_000 })

  const [section, setSection] = useState<SectionId>('catalogo')
  const [selectedRelease, setSelectedRelease] = useState<string | null>(null)
  const [trackedRunId, setTrackedRunId] = useState<string | null>(null)
  const [ingestOpen, setIngestOpen] = useState(false)
  const [setVersionOpen, setSetVersionOpen] = useState(false)

  // Reentrada de tela: se já existe um run em andamento, acompanhar ele — não esquecer o que está
  // rolando só porque a pessoa recarregou a página ou voltou mais tarde. SÓ UMA VEZ (ref, não
  // state): achado real do Codex — sem isto, clicar em "Fechar acompanhamento" (que também zera
  // trackedRunId) fazia este efeito reabrir o MESMO run na hora, e "fechar" nunca funcionava
  // enquanto o run continuasse ativo.
  //
  // Achado Codex [P2] (2a rodada): a ref só era marcada DENTRO deste efeito — se a tela abriu
  // sem nenhum run ativo (ref nunca marcada) e a pessoa iniciar uma atualização NESTA mesma
  // visita (StartRun chama trackRun diretamente, nunca este efeito), fechar o acompanhamento
  // reabria o MESMO run de novo, pelo mesmo motivo original. trackRun agora é o ÚNICO jeito de
  // escolher um run (reentrada automática OU escolha manual), e sempre marca a ref — reentrada
  // automática só pode acontecer uma vez por visita à tela, não importa por qual caminho.
  const autoResumedRef = useRef(false)
  const trackRun = (id: string | null) => {
    autoResumedRef.current = true
    setTrackedRunId(id)
  }
  useEffect(() => {
    if (autoResumedRef.current || trackedRunId || !runs.data) return
    const active = runs.data.find((r) => !isTerminalRunState(r.state))
    if (active) {
      trackRun(active.id)
      setSection('atualizacoes')
    }
  }, [runs.data, trackedRunId])

  useEffect(() => {
    if ([installation.error, releases.error, runs.error].some(isUnauthorized)) handleUnauthorized()
  }, [installation.error, releases.error, runs.error])

  const setVersionMutation = useMutation({
    mutationFn: (version: string) => platformInstallationAPI.setVersion(version, crypto.randomUUID()),
    onSuccess: () => {
      showToast('Versão instalada registrada.')
      setSetVersionOpen(false)
      void qc.invalidateQueries({ queryKey: ['platform-installation'] })
    },
    onError: (err) => showToast(describePlatformReleaseError(err), 'error'),
  })

  const ingestMutation = useMutation({
    mutationFn: (manifest: unknown) => platformReleasesAPI.ingest(manifest as never, crypto.randomUUID()),
    onSuccess: () => {
      showToast('Release adicionada ao catálogo.')
      setIngestOpen(false)
      void qc.invalidateQueries({ queryKey: ['platform-releases'] })
    },
    onError: (err) => showToast(describePlatformReleaseError(err), 'error'),
  })

  if (installation.isLoading) return <div className="p-6"><LoadingState message="Verificando acesso de operador de plataforma…" /></div>
  if (installation.isError) {
    if ((installation.error as { response?: { status?: number } })?.response?.status === 404) {
      return <div className="p-6"><PermissionState message="Esta área é só para operadores de plataforma (ADR-0038). Se você deveria ter acesso, peça a um operador existente para te adicionar." /></div>
    }
    return <div className="p-6"><ErrorState message="Não foi possível carregar o estado da instalação." action={{ label: 'Tentar novamente', onClick: () => void installation.refetch() }} /></div>
  }
  const inst = installation.data
  if (!inst) return null

  return (
    <div className="flex min-h-full flex-col">
      <PageHeader
        breadcrumbs={[{ label: 'Administração global' }, { label: 'Plataforma', current: true }]}
        title="Versões e atualizações"
        description="Catálogo de releases, preflight de compatibilidade e acompanhamento de atualizações da instalação OMNIRA — escopo global, nunca por tenant ou Hub."
      />
      <div className="mx-auto w-full max-w-5xl flex-1 space-y-6 p-4 sm:p-6">
        <Card>
          <CardHeader>
            <h2 className="text-section-md font-semibold text-text-primary">Instalação atual</h2>
          </CardHeader>
          <CardBody>
            <dl className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
              <Field label="Canal" value={inst.channel} />
              <Field label="Versão instalada" value={inst.current_version || 'não registrada'} warn={!inst.current_version} />
              <Field label="Protocolo do agente" value={String(inst.agent_protocol)} />
              <Field label="Atualizado em" value={new Date(inst.updated_at).toLocaleString('pt-BR')} />
            </dl>
            {!inst.current_version && (
              <p className="mt-4 text-body-sm text-status-warning-strong">
                Sem a versão instalada registrada, o preflight bloqueia toda release por padrão (fail-closed).
              </p>
            )}
            <div className="mt-4">
              <Button variant="secondary" size="sm" onClick={() => setSetVersionOpen(true)}>
                Registrar versão instalada
              </Button>
            </div>
          </CardBody>
        </Card>

        <Tabs items={TAB_ITEMS} value={section} onChange={setSection} aria-label="Seções de Plataforma" />

        {section === 'catalogo' && (
          <CatalogSection
            releases={releases}
            selectedRelease={selectedRelease}
            onSelect={setSelectedRelease}
            onIngest={() => setIngestOpen(true)}
            onRunStarted={(run) => {
              trackRun(run.id)
              setSection('atualizacoes')
              void qc.invalidateQueries({ queryKey: ['platform-runs'] })
            }}
            installedVersion={inst.current_version}
          />
        )}

        {section === 'atualizacoes' && (
          <UpdatesSection
            runs={runs}
            trackedRunId={trackedRunId}
            onTrack={trackRun}
          />
        )}
      </div>

      <Modal open={setVersionOpen} title="Registrar versão instalada" onClose={() => setSetVersionOpen(false)}>
        <SetVersionForm isPending={setVersionMutation.isPending} onSubmit={(v) => setVersionMutation.mutate(v)} onCancel={() => setSetVersionOpen(false)} />
      </Modal>

      <Modal open={ingestOpen} title="Ingerir release" description="Cole o manifesto (JSON) já obtido de um build assinado. O servidor valida o formato estrito antes de guardar." onClose={() => setIngestOpen(false)}>
        <IngestForm isPending={ingestMutation.isPending} onSubmit={(m) => ingestMutation.mutate(m)} onCancel={() => setIngestOpen(false)} />
      </Modal>
    </div>
  )
}

function Field({ label, value, warn }: { label: string; value: string; warn?: boolean }) {
  return (
    <div>
      <dt className="text-body-sm text-text-secondary">{label}</dt>
      <dd className={warn ? 'font-medium text-status-warning-strong' : 'font-medium text-text-primary'}>{value}</dd>
    </div>
  )
}

function SetVersionForm({ isPending, onSubmit, onCancel }: { isPending: boolean; onSubmit: (version: string) => void; onCancel: () => void }) {
  const [version, setVersion] = useState('')
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        if (version.trim()) onSubmit(version.trim())
      }}
      className="space-y-4"
    >
      <Input label="Versão" placeholder="ex.: 2026.10.0" value={version} onChange={(e) => setVersion(e.target.value)} autoFocus />
      <div className="flex justify-end gap-2">
        <Button type="button" variant="secondary" size="sm" onClick={onCancel} disabled={isPending}>Cancelar</Button>
        <Button type="submit" size="sm" isLoading={isPending} disabled={!version.trim()}>Registrar</Button>
      </div>
    </form>
  )
}

function IngestForm({ isPending, onSubmit, onCancel }: { isPending: boolean; onSubmit: (manifest: unknown) => void; onCancel: () => void }) {
  const [text, setText] = useState('')
  const [parseError, setParseError] = useState('')
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        try {
          const parsed = JSON.parse(text)
          setParseError('')
          onSubmit(parsed)
        } catch {
          setParseError('JSON inválido. Confira o manifesto antes de enviar.')
        }
      }}
      className="space-y-4"
    >
      <TextArea label="Manifesto (JSON)" rows={12} value={text} onChange={(e) => setText(e.target.value)} error={parseError} className="font-mono text-xs" autoFocus />
      <div className="flex justify-end gap-2">
        <Button type="button" variant="secondary" size="sm" onClick={onCancel} disabled={isPending}>Cancelar</Button>
        <Button type="submit" size="sm" isLoading={isPending} disabled={!text.trim()}>Ingerir</Button>
      </div>
    </form>
  )
}

function CatalogSection({
  releases,
  selectedRelease,
  onSelect,
  onIngest,
  onRunStarted,
  installedVersion,
}: {
  releases: ReturnType<typeof useQuery<PlatformRelease[]>>
  selectedRelease: string | null
  onSelect: (id: string | null) => void
  onIngest: () => void
  onRunStarted: (run: PlatformUpdateRun) => void
  installedVersion?: string
}) {
  if (releases.isLoading) return <LoadingState message="Carregando catálogo…" />
  if (releases.isError) return <ErrorState message="Não foi possível carregar o catálogo de releases." action={{ label: 'Tentar novamente', onClick: () => void releases.refetch() }} />
  const items = releases.data ?? []
  const selected = items.find((r) => r.release_id === selectedRelease) ?? null

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h2 className="text-section-md font-semibold text-text-primary">Catálogo de releases</h2>
        <Button size="sm" onClick={onIngest}>Ingerir release</Button>
      </div>
      {items.length === 0 ? (
        <EmptyState title="Nenhuma release no catálogo" description="Ingerida uma release (manifesto assinado de um build real), ela aparece aqui para preflight e atualização." action={{ label: 'Ingerir release', onClick: onIngest }} />
      ) : (
        <Table aria-label="Releases ingeridas">
          <TableHead>
            <TableRow>
              <TableHeaderCell>Release</TableHeaderCell>
              <TableHeaderCell>Canal</TableHeaderCell>
              <TableHeaderCell>Versão</TableHeaderCell>
              <TableHeaderCell>Ingerida em</TableHeaderCell>
              <TableHeaderCell><span className="sr-only">Ações</span></TableHeaderCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {items.map((r) => (
              <TableRow key={r.release_id}>
                <TableCell>{r.release_id}</TableCell>
                <TableCell><Badge size="sm" variant={r.channel === 'stable' ? 'success' : 'info'}>{r.channel}</Badge></TableCell>
                <TableCell>{r.platform_version}</TableCell>
                <TableCell>{new Date(r.ingested_at).toLocaleString('pt-BR')}</TableCell>
                <TableCell>
                  <Button size="sm" variant="tertiary" onClick={() => onSelect(selectedRelease === r.release_id ? null : r.release_id)}>
                    {selectedRelease === r.release_id ? 'Fechar' : 'Detalhes'}
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      {selected && (
        // key: trocar de release precisa de um ReleaseDetail NOVO, nunca o plano/confirmação
        // de uma release anterior sobrevivendo com o rótulo de outra (achado real do Codex).
        <ReleaseDetail key={selected.release_id} release={selected} installedVersion={installedVersion} onRunStarted={onRunStarted} />
      )}
    </div>
  )
}

function ReleaseDetail({ release, installedVersion, onRunStarted }: { release: PlatformRelease; installedVersion?: string; onRunStarted: (run: PlatformUpdateRun) => void }) {
  const [plan, setPlan] = useState<PlatformUpdatePlan | null>(null)
  const [confirmOpen, setConfirmOpen] = useState(false)
  const m = release.manifest

  const preflightMutation = useMutation({
    mutationFn: () => platformPlansAPI.create(release.release_id, crypto.randomUUID()),
    onSuccess: (p) => setPlan(p),
    onError: (err) => showToast(describePlatformReleaseError(err), 'error'),
  })
  const startMutation = useMutation({
    mutationFn: () => platformRunsAPI.start(plan!.id, crypto.randomUUID()),
    onSuccess: (run) => {
      setConfirmOpen(false)
      showToast('Atualização iniciada.')
      onRunStarted(run)
    },
    onError: (err) => {
      setConfirmOpen(false)
      showToast(describePlatformReleaseError(err), 'error')
    },
  })

  return (
    <Card>
      <CardHeader>
        <h3 className="text-section-md font-semibold text-text-primary">{release.release_id}</h3>
      </CardHeader>
      <CardBody className="space-y-6">
        <section aria-label="Changelog">
          <h4 className="text-body-sm font-semibold text-text-secondary">Changelog</h4>
          {m.release_notes_ref ? (
            <a href={m.release_notes_ref} target="_blank" rel="noreferrer" className="text-sm font-medium text-accent-primary underline-offset-2 hover:underline">
              {m.release_notes_ref}
            </a>
          ) : (
            <p className="text-body-sm text-text-tertiary">Sem link de changelog nesta release.</p>
          )}
        </section>
        <section aria-label="Diferenças">
          <h4 className="text-body-sm font-semibold text-text-secondary">Diferenças (diff)</h4>
          <ul className="mt-1 list-disc space-y-1 pl-5 text-body-sm text-text-primary">
            <li>Módulos alterados: {m.modules_changed.length ? m.modules_changed.join(', ') : 'nenhum declarado'}</li>
            <li>Artefatos: {m.artifacts.map((a) => `${a.unit} (${a.image})`).join(', ') || 'nenhum'}</li>
            <li>Migrations: {m.migrations.start_after} → {m.migrations.target}</li>
          </ul>
        </section>
        <section aria-label="Riscos">
          <h4 className="text-body-sm font-semibold text-text-secondary">Riscos e compatibilidade</h4>
          <ul className="mt-1 list-disc space-y-1 pl-5 text-body-sm text-text-primary">
            <li>Versões de origem suportadas: {m.compatibility.from_versions.join(', ') || 'nenhuma declarada'}</li>
            <li>Protocolo mínimo do agente: {m.compatibility.min_agent_protocol}</li>
            <li>Estratégia de banco: {m.compatibility.database_strategy}</li>
            <li>Classe de recuperação: {m.rollout.recovery_class}</li>
            {installedVersion && !m.compatibility.from_versions.includes(installedVersion) && (
              <li className="font-medium text-status-danger">A versão instalada ({installedVersion}) não está nas versões de origem suportadas.</li>
            )}
          </ul>
        </section>

        <section aria-label="Preflight" className="space-y-3 border-t border-border-subtle pt-4">
          <div className="flex items-center justify-between">
            <h4 className="text-body-sm font-semibold text-text-secondary">Preflight</h4>
            <Button size="sm" variant="secondary" onClick={() => preflightMutation.mutate()} isLoading={preflightMutation.isPending}>
              Rodar preflight
            </Button>
          </div>
          {plan && (
            <div className="rounded-control border border-border-subtle p-4">
              <Badge variant={plan.preflight_status === 'PASS' ? 'success' : 'danger'}>{plan.preflight_status}</Badge>
              {plan.preflight_detail.reasons.length > 0 && (
                <ul className="mt-2 list-disc space-y-1 pl-5 text-body-sm text-text-secondary">
                  {plan.preflight_detail.reasons.map((reason, i) => <li key={i}>{reason}</li>)}
                </ul>
              )}
              <p className="mt-2 text-body-sm text-text-tertiary">Plano válido até {new Date(plan.expires_at).toLocaleString('pt-BR')}.</p>
              {plan.preflight_status === 'PASS' && (
                <div className="mt-4">
                  <Button onClick={() => setConfirmOpen(true)}>Iniciar atualização (escopo global)</Button>
                </div>
              )}
            </div>
          )}
        </section>
      </CardBody>

      <ConfirmDialog
        open={confirmOpen}
        title="Confirmar atualização de escopo global"
        message={`Esta ação atualiza a instalação OMNIRA inteira (todos os tenants, não um tenant específico) para ${release.platform_version} (${release.release_id}). O servidor revalida a compatibilidade de novo antes de despachar ao agente.`}
        confirmLabel="Iniciar atualização"
        isPending={startMutation.isPending}
        onConfirm={() => startMutation.mutate()}
        onCancel={() => setConfirmOpen(false)}
      />
    </Card>
  )
}

function UpdatesSection({
  runs,
  trackedRunId,
  onTrack,
}: {
  runs: ReturnType<typeof useQuery<PlatformUpdateRun[]>>
  trackedRunId: string | null
  onTrack: (id: string | null) => void
}) {
  if (runs.isLoading) return <LoadingState message="Carregando histórico de atualizações…" />
  if (runs.isError) return <ErrorState message="Não foi possível carregar o histórico de atualizações." action={{ label: 'Tentar novamente', onClick: () => void runs.refetch() }} />
  const items = runs.data ?? []

  return (
    <div className="space-y-6">
      {trackedRunId && (
        // key: trocar de run precisa de um RunTracking NOVO — senão a trilha acumulada e o
        // cursor `after` do run anterior sobrevivem e o run novo perde seus primeiros eventos
        // (achado real do Codex).
        <RunTracking key={trackedRunId} runId={trackedRunId} onClose={() => onTrack(null)} />
      )}

      <div>
        <h2 className="text-section-md font-semibold text-text-primary">Histórico</h2>
        {items.length === 0 ? (
          <EmptyState title="Nenhuma atualização ainda" description="Execuções de atualização desta instalação aparecem aqui, mais recentes primeiro." />
        ) : (
          <Table aria-label="Histórico de atualizações">
            <TableHead>
              <TableRow>
                <TableHeaderCell>Execução</TableHeaderCell>
                <TableHeaderCell>Release</TableHeaderCell>
                <TableHeaderCell>Estado</TableHeaderCell>
                <TableHeaderCell>Atualizada em</TableHeaderCell>
                <TableHeaderCell><span className="sr-only">Ações</span></TableHeaderCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {items.map((run) => (
                <TableRow key={run.id}>
                  <TableCell>{run.id}</TableCell>
                  <TableCell>{run.release_id}</TableCell>
                  <TableCell><RunStateBadge state={run.state} /></TableCell>
                  <TableCell>{new Date(run.updated_at).toLocaleString('pt-BR')}</TableCell>
                  <TableCell>
                    <Button size="sm" variant="tertiary" onClick={() => onTrack(run.id)}>Acompanhar</Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </div>
    </div>
  )
}

function RunStateBadge({ state }: { state: string }) {
  const danger = ['FAILED', 'RECOVERY_REQUIRED', 'BLOCKED', 'DISPATCH_CONFLICT', 'COMPATIBILITY_VOIDED'].includes(state)
  const success = state === 'SUCCEEDED'
  const variant = success ? 'success' : danger ? 'danger' : isTerminalRunState(state) ? 'default' : 'info'
  return <Badge variant={variant}>{runStateLabel(state)}</Badge>
}

const CHECKPOINT_STATUS_LABEL: Record<string, string> = { started: 'iniciado', completed: 'concluído', failed: 'falhou' }

// Achado Codex [P2]: os checkpoints do Update Agent (internal/platformrelease/updatemgr
// refreshFromLedger) sempre gravam kind="checkpoint" — o passo (step), o resultado (status:
// started/completed/failed) e a mensagem de diagnóstico ficam só em `detail`. Mostrar apenas
// "checkpoint" + hora tornava toda linha indistinguível e escondia o motivo de uma falha/
// recuperação. "run.locked" não tem step/status (é o marcador de despacho, não um checkpoint do
// agente), então mostra só o kind mesmo.
function EventLine({ event }: { event: { seq: number; kind: string; detail: Record<string, unknown>; created_at: string } }) {
  const step = typeof event.detail.step === 'string' ? event.detail.step : undefined
  const status = typeof event.detail.status === 'string' ? event.detail.status : undefined
  const detailText = typeof event.detail.detail === 'string' ? event.detail.detail : undefined
  if (event.kind !== 'checkpoint' || !step) {
    return (
      <>
        <span className="font-medium text-text-primary">{event.kind}</span>{' '}
        <span className="text-text-tertiary">{new Date(event.created_at).toLocaleString('pt-BR')}</span>
      </>
    )
  }
  return (
    <div>
      <span className="font-medium text-text-primary">{step}</span>{' '}
      {status && <Badge size="sm" variant={status === 'failed' ? 'danger' : status === 'completed' ? 'success' : 'info'}>{CHECKPOINT_STATUS_LABEL[status] ?? status}</Badge>}{' '}
      <span className="text-text-tertiary">{new Date(event.created_at).toLocaleString('pt-BR')}</span>
      {detailText && <p className="mt-1 text-text-secondary">{detailText}</p>}
    </div>
  )
}

// Acompanhamento por polling real (SSE não existe no backend desta fase — ver F6/F7 em
// IMPLEMENTATION-STATUS.md): refaz a consulta do run e busca só os eventos novos (cursor `after`)
// enquanto o estado não for terminal; para de perguntar sozinho quando o run termina.
function RunTracking({ runId, onClose }: { runId: string; onClose: () => void }) {
  const qc = useQueryClient()
  const run = useQuery({ queryKey: ['platform-run', runId], queryFn: () => platformRunsAPI.get(runId), retry: false })
  const terminal = run.data ? isTerminalRunState(run.data.state) : false

  const [all, setAll] = useState<{ seq: number; kind: string; detail: Record<string, unknown>; created_at: string }[]>([])
  const lastSeq = all.length ? all[all.length - 1].seq : -1
  const events = useQuery({
    // lastSeq in the key: each new cursor value is its own cache entry, so a poll always asks
    // the server for only what is new (the `after` cursor), never re-fetching the whole trail.
    queryKey: ['platform-run-events', runId, lastSeq],
    queryFn: () => platformRunsAPI.events(runId, lastSeq >= 0 ? lastSeq : undefined),
    refetchInterval: terminal ? false : 4_000,
    retry: false,
  })
  useEffect(() => {
    if (!events.data || events.data.length === 0) return
    setAll((prev) => {
      const seen = new Set(prev.map((e) => e.seq))
      const added = events.data.filter((e) => !seen.has(e.seq))
      if (added.length === 0) return prev
      return [...prev, ...added].sort((a, b) => a.seq - b.seq)
    })
  }, [events.data])

  useEffect(() => {
    const id = terminal ? undefined : setInterval(() => void run.refetch(), 4_000)
    return () => { if (id) clearInterval(id) }
  }, [terminal, run])

  // Achado Codex [P2]: o run pode virar terminal bem entre uma consulta de eventos e a
  // próxima — como refetchInterval para assim que `terminal` é verdade, o checkpoint final
  // (sucesso, falha ou o motivo da recuperação) podia nunca chegar a esta tela. Uma última
  // busca, só na transição para terminal (nunca de novo), cobre essa janela. Sucesso também
  // invalida a instalação: sem isto, "Versão instalada" e o preflight de outras releases
  // continuavam comparando contra a versão ANTERIOR mesmo depois da atualização confirmada.
  const terminalHandledRef = useRef(false)
  useEffect(() => {
    if (!terminal || terminalHandledRef.current) return
    terminalHandledRef.current = true
    void events.refetch()
    if (run.data?.state === 'SUCCEEDED') void qc.invalidateQueries({ queryKey: ['platform-installation'] })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [terminal])

  if (run.isLoading) return <LoadingState message="Carregando execução…" />
  if (run.isError) {
    return <ErrorState message={describePlatformReleaseError(run.error)} action={{ label: 'Fechar', onClick: onClose }} />
  }
  const r = run.data!

  return (
    <Card>
      <CardHeader className="flex items-center justify-between">
        <div>
          <h3 className="text-section-md font-semibold text-text-primary">Acompanhando {r.id}</h3>
          <p className="text-body-sm text-text-secondary">Release {r.release_id}</p>
        </div>
        <Button size="sm" variant="tertiary" onClick={onClose}>Fechar acompanhamento</Button>
      </CardHeader>
      <CardBody className="space-y-4">
        <RunStateBadge state={r.state} />
        {r.state === 'RECOVERY_REQUIRED' && (
          <ErrorState title="Recuperação manual necessária" message="O Update Agent não processa nenhum run novo desta instalação até a recuperação ser reconhecida (updatectl/runbook de operações)." />
        )}
        <div>
          <h4 className="text-body-sm font-semibold text-text-secondary">Trilha de eventos{terminal ? '' : ' (atualizando…)'}</h4>
          {all.length === 0 ? (
            <p className="text-body-sm text-text-tertiary">Nenhum evento ainda.</p>
          ) : (
            <ol className="mt-2 space-y-2">
              {all.map((e) => (
                <li key={e.seq} className="rounded-control border border-border-subtle p-3 text-body-sm">
                  <EventLine event={e} />
                </li>
              ))}
            </ol>
          )}
          {events.isError && (
            <div className="mt-2">
              <ErrorState
                message={`Não foi possível buscar os eventos mais recentes: ${describePlatformReleaseError(events.error)}`}
                action={{ label: 'Tentar novamente', onClick: () => void events.refetch() }}
              />
            </div>
          )}
        </div>
      </CardBody>
    </Card>
  )
}
