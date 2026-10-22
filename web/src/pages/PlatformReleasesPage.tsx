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
import { currentUserId, handleUnauthorized, isUnauthorized } from '../lib/session'
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

// Achado Codex [P2] (8a rodada/9a rodada de revisão): `crypto.randomUUID()` chamado DENTRO de
// cada mutationFn gera uma chave NOVA em toda tentativa, inclusive num retry depois de uma
// falha de rede/timeout. Para StartRun especificamente isso é grave: se o servidor já reservou
// o pedido (REQUESTED) mas o despacho falhou de forma transitória, um retry com chave DIFERENTE
// nunca re-tenta o MESMO pedido (só Idempotency-Key repetida faz o servidor retomar/responder de
// novo) — cai no gate de "só um run ativo por instalação" e fica travado sem nenhum caminho de
// recuperação nesta tela. A mesma chave precisa sobreviver a um retry da MESMA ação; só depois
// de um SUCESSO (ou de a pessoa desistir e fechar) é que a próxima tentativa é, de verdade, um
// pedido novo e merece uma chave nova.
// Achado Codex [P2] (10a rodada): reter a chave entre tentativas (acima) resolve o retry da
// MESMA requisição, mas StartRun especificamente pode legitimamente mudar de ALVO entre
// tentativas — uma primeira tentativa falha, a pessoa roda o preflight de novo e recebe um
// plano NOVO (plan_id diferente); reusar a chave antiga (presa ao plano antigo) faz o servidor
// devolver ErrKeyMismatch (StartRun compara a chave contra o request_hash gravado, que inclui o
// plan_id) para TODA tentativa seguinte, travado até o componente desmontar. `scope` (aqui, o
// próprio plan_id) faz a chave acompanhar o alvo real: muda de plano, muda de chave; mesmo
// plano, mesma chave.
const DEFAULT_SCOPE = '_default'
function useStableIdempotencyKey() {
  const ref = useRef<{ scope: string; key: string } | null>(null)
  const get = (scope: string = DEFAULT_SCOPE) => {
    if (!ref.current || ref.current.scope !== scope) ref.current = { scope, key: crypto.randomUUID() }
    return ref.current.key
  }
  const spend = () => { ref.current = null }
  return { get, spend }
}

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

  // Achado Codex [P2]: as duas listas só mostravam a primeira página (50), sem nenhum jeito de
  // ver releases/runs mais antigos — o cursor `before` já existe no cliente e no servidor, só
  // não era usado. "Carregar mais" ACRESCENTA à primeira página (releases/runs acima), nunca a
  // substitui: a reentrada e o polling de 10s continuam lendo só a primeira página, intactos.
  const PAGE_SIZE = 50
  const [olderReleases, setOlderReleases] = useState<PlatformRelease[]>([])
  // null until the first "load more" click: before that, whether there IS more is derived
  // straight from the first page's own size (never an optimistic guess — a first page smaller
  // than PAGE_SIZE never offers the button at all).
  const [extraReleasesHasMore, setExtraReleasesHasMore] = useState<boolean | null>(null)
  const hasMoreReleases = extraReleasesHasMore ?? (releases.data?.length ?? 0) >= PAGE_SIZE
  // Achado Codex [P2] (7a rodada): a reset acima por si só não bastava — se o "load more" já
  // estivesse EM VOO (cursor calculado a partir da fronteira ANTIGA) no exato momento em que a
  // primeira página muda, a resposta chegava DEPOIS da reset e a reinseria no acumulado, criando
  // de volta o mesmo buraco silencioso. Toda resposta carrega a geração em que foi PEDIDA; se a
  // geração atual já avançou quando ela chega, é descartada.
  const releasesGenerationRef = useRef(0)
  const loadMoreReleases = useMutation({
    mutationFn: async () => {
      const generation = releasesGenerationRef.current
      const last = (olderReleases.length ? olderReleases : releases.data ?? [])[olderReleases.length ? olderReleases.length - 1 : (releases.data?.length ?? 1) - 1]
      const page = await platformReleasesAPI.list(last?.ingested_at)
      return { page, generation }
    },
    onSuccess: ({ page, generation }) => {
      if (generation !== releasesGenerationRef.current) return
      setOlderReleases((prev) => [...prev, ...page])
      setExtraReleasesHasMore(page.length >= PAGE_SIZE)
    },
    onError: (err) => showToast(describePlatformReleaseError(err), 'error'),
  })
  // Achado Codex [P2] (6a rodada): se a primeira página mudar (nova release ingerida — ou, no
  // caso de runs, um novo run aparecendo via o polling de 10s) DEPOIS de já ter carregado mais
  // páginas, a antiga última entrada da primeira página cai num buraco: some da página 1
  // (empurrada pela entrada nova) e nunca estava na página 2 (que começa ONDE a antiga página 1
  // terminava). Detectado pela mudança do item do TOPO (o único jeito de uma entrada nova
  // aparecer, já que a ordem é por ingested_at/created_at, que nunca muda para uma entrada já
  // existente) — reinicia o que foi carregado a mais em vez de arriscar um buraco silencioso.
  const prevTopReleaseIdRef = useRef<string | undefined>(undefined)
  const topReleaseId = releases.data?.[0]?.release_id
  useEffect(() => {
    if (prevTopReleaseIdRef.current !== undefined && topReleaseId !== undefined && topReleaseId !== prevTopReleaseIdRef.current) {
      releasesGenerationRef.current += 1
      if (olderReleases.length > 0) {
        setOlderReleases([])
        setExtraReleasesHasMore(null)
        showToast('O catálogo foi atualizado — carregue mais de novo se precisar ver releases mais antigas.', 'info')
      }
    }
    prevTopReleaseIdRef.current = topReleaseId
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [topReleaseId])

  const [olderRuns, setOlderRuns] = useState<PlatformUpdateRun[]>([])
  const [extraRunsHasMore, setExtraRunsHasMore] = useState<boolean | null>(null)
  const hasMoreRuns = extraRunsHasMore ?? (runs.data?.length ?? 0) >= PAGE_SIZE
  const runsGenerationRef = useRef(0)
  const loadMoreRuns = useMutation({
    mutationFn: async () => {
      const generation = runsGenerationRef.current
      const last = (olderRuns.length ? olderRuns : runs.data ?? [])[olderRuns.length ? olderRuns.length - 1 : (runs.data?.length ?? 1) - 1]
      const page = await platformRunsAPI.list(last?.created_at)
      return { page, generation }
    },
    onSuccess: ({ page, generation }) => {
      if (generation !== runsGenerationRef.current) return
      setOlderRuns((prev) => [...prev, ...page])
      setExtraRunsHasMore(page.length >= PAGE_SIZE)
    },
    onError: (err) => showToast(describePlatformReleaseError(err), 'error'),
  })
  // Mesmo achado acima, para o histórico: um run novo (via o polling de 10s) empurra a antiga
  // última entrada da primeira página para o mesmo buraco silencioso.
  const prevTopRunIdRef = useRef<string | undefined>(undefined)
  const topRunId = runs.data?.[0]?.id
  useEffect(() => {
    if (prevTopRunIdRef.current !== undefined && topRunId !== undefined && topRunId !== prevTopRunIdRef.current) {
      runsGenerationRef.current += 1
      if (olderRuns.length > 0) {
        setOlderRuns([])
        setExtraRunsHasMore(null)
        showToast('O histórico foi atualizado — carregue mais de novo se precisar ver execuções mais antigas.', 'info')
      }
    }
    prevTopRunIdRef.current = topRunId
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [topRunId])

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

  const setVersionKey = useStableIdempotencyKey()
  const setVersionMutation = useMutation({
    // Achado Codex [P2] (11a rodada): sem escopo, uma resposta perdida (o servidor gravou, mas
    // o cliente nunca viu o 200 — um erro de rede, não de aplicação) deixava a chave presa ao
    // valor SUBMETIDO; editar o campo e reenviar reusaria a mesma chave para um valor
    // DIFERENTE, batendo em ErrKeyMismatch sem nenhum jeito de corrigir a não ser fechar e
    // reabrir o modal. Escopar por `version` (o mesmo padrão já usado por StartRun/plan_id):
    // reenviar o MESMO valor reusa a chave (retry correto), editar o valor ganha uma chave nova.
    mutationFn: (version: string) => platformInstallationAPI.setVersion(version, setVersionKey.get(version)),
    onSuccess: () => {
      setVersionKey.spend()
      showToast('Versão instalada registrada.')
      setSetVersionOpen(false)
      void qc.invalidateQueries({ queryKey: ['platform-installation'] })
    },
    onError: (err) => showToast(describePlatformReleaseError(err), 'error'),
  })

  const ingestKey = useStableIdempotencyKey()
  const ingestMutation = useMutation({
    // Mesmo achado acima, para o manifesto colado: escopado pelo JSON exato submetido (via
    // JSON.stringify estável o bastante aqui — o ponto é distinguir "mesmo texto" de "texto
    // editado", nunca comparar semanticamente).
    mutationFn: (manifest: unknown) => platformReleasesAPI.ingest(manifest as never, ingestKey.get(JSON.stringify(manifest))),
    onSuccess: () => {
      ingestKey.spend()
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
            olderReleases={olderReleases}
            hasMoreReleases={hasMoreReleases}
            onLoadMore={() => loadMoreReleases.mutate()}
            isLoadingMore={loadMoreReleases.isPending}
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
            olderRuns={olderRuns}
            hasMoreRuns={hasMoreRuns}
            onLoadMore={() => loadMoreRuns.mutate()}
            isLoadingMore={loadMoreRuns.isPending}
            trackedRunId={trackedRunId}
            onTrack={trackRun}
          />
        )}
      </div>

      <Modal open={setVersionOpen} title="Registrar versão instalada" onClose={() => { setVersionKey.spend(); setSetVersionOpen(false) }}>
        <SetVersionForm isPending={setVersionMutation.isPending} onSubmit={(v) => setVersionMutation.mutate(v)} onCancel={() => { setVersionKey.spend(); setSetVersionOpen(false) }} />
      </Modal>

      <Modal open={ingestOpen} title="Ingerir release" description="Cole o manifesto (JSON) já obtido de um build assinado. O servidor valida o formato estrito antes de guardar." onClose={() => { ingestKey.spend(); setIngestOpen(false) }}>
        <IngestForm isPending={ingestMutation.isPending} onSubmit={(m) => ingestMutation.mutate(m)} onCancel={() => { ingestKey.spend(); setIngestOpen(false) }} />
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
  olderReleases,
  hasMoreReleases,
  onLoadMore,
  isLoadingMore,
  selectedRelease,
  onSelect,
  onIngest,
  onRunStarted,
  installedVersion,
}: {
  releases: ReturnType<typeof useQuery<PlatformRelease[]>>
  olderReleases: PlatformRelease[]
  hasMoreReleases: boolean
  onLoadMore: () => void
  isLoadingMore: boolean
  selectedRelease: string | null
  onSelect: (id: string | null) => void
  onIngest: () => void
  onRunStarted: (run: PlatformUpdateRun) => void
  installedVersion?: string
}) {
  if (releases.isLoading) return <LoadingState message="Carregando catálogo…" />
  if (releases.isError) return <ErrorState message="Não foi possível carregar o catálogo de releases." action={{ label: 'Tentar novamente', onClick: () => void releases.refetch() }} />
  const items = [...(releases.data ?? []), ...olderReleases]
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
      {items.length > 0 && hasMoreReleases && (
        <div className="flex justify-center">
          <Button variant="secondary" size="sm" onClick={onLoadMore} isLoading={isLoadingMore}>Carregar mais releases</Button>
        </div>
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

  const preflightKey = useStableIdempotencyKey()
  const preflightMutation = useMutation({
    mutationFn: () => platformPlansAPI.create(release.release_id, preflightKey.get()),
    onSuccess: (p) => { preflightKey.spend(); setPlan(p) },
    onError: (err) => showToast(describePlatformReleaseError(err), 'error'),
  })
  const startKey = useStableIdempotencyKey()
  const startMutation = useMutation({
    mutationFn: () => platformRunsAPI.start(plan!.id, startKey.get(plan!.id)),
    onSuccess: (run) => {
      startKey.spend()
      setConfirmOpen(false)
      showToast('Atualização iniciada.')
      onRunStarted(run)
    },
    onError: (err) => {
      // O diálogo fecha, mas a CHAVE sobrevive ao erro — achado Codex [P2] (8a/9a rodada):
      // se o servidor já reservou o pedido (REQUESTED) e o despacho falhou de forma
      // transitória, clicar em "Iniciar atualização" de novo precisa reusar a MESMA chave
      // para o servidor retomar o MESMO pedido, nunca criar um segundo em conflito com o
      // primeiro (gate de "só um run ativo por instalação").
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
              {/* Achado Codex [P2] (12a rodada): backup_evidence é SÓ aviso (nunca bloqueia em
                  F6 — o gate real é F9), então um plano PASS com backup ausente/desatualizado
                  não aparecia em `reasons`. Sem isto, a pessoa via PASS e podia confirmar sem
                  nunca ver o aviso que o próprio servidor já calculou. Mostrado SEMPRE que
                  existir, PASS ou BLOCK, antes do botão de confirmar. */}
              {plan.preflight_detail.backup_evidence?.warn && (
                <div className="mt-2 rounded-control border border-status-warning-border bg-status-warning-soft p-3 text-body-sm text-status-warning-strong">
                  {plan.preflight_detail.backup_evidence.warn}
                </div>
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
  olderRuns,
  hasMoreRuns,
  onLoadMore,
  isLoadingMore,
  trackedRunId,
  onTrack,
}: {
  runs: ReturnType<typeof useQuery<PlatformUpdateRun[]>>
  olderRuns: PlatformUpdateRun[]
  hasMoreRuns: boolean
  onLoadMore: () => void
  isLoadingMore: boolean
  trackedRunId: string | null
  onTrack: (id: string | null) => void
}) {
  if (runs.isLoading) return <LoadingState message="Carregando histórico de atualizações…" />
  if (runs.isError) return <ErrorState message="Não foi possível carregar o histórico de atualizações." action={{ label: 'Tentar novamente', onClick: () => void runs.refetch() }} />
  const items = [...(runs.data ?? []), ...olderRuns]

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
        {items.length > 0 && hasMoreRuns && (
          <div className="mt-4 flex justify-center">
            <Button variant="secondary" size="sm" onClick={onLoadMore} isLoading={isLoadingMore}>Carregar mais execuções</Button>
          </div>
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

  // Achado Codex [P2] (10a rodada): dependia do objeto `run` INTEIRO — o react-query devolve
  // uma referência NOVA a cada render (inclusive os causados pela query de eventos, que chega
  // separada e mais frequente), então este efeito limpava e recriava o intervalo em TODO
  // render. Com eventos chegando mais rápido que 4s, o intervalo nunca sobrevivia os 4s
  // inteiros e run.refetch() nunca era chamado — o estado do run ficava parado mesmo com a
  // tela dizendo "atualizando…". `run.refetch` é estável entre renders (método do MESMO
  // observador da query); só ele (e `terminal`) precisam disparar o efeito de novo.
  useEffect(() => {
    const id = terminal ? undefined : setInterval(() => void run.refetch(), 4_000)
    return () => { if (id) clearInterval(id) }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [terminal, run.refetch])

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

  // Achado Codex [P2] (11a rodada): a chave de StartRun só sobrevivia DENTRO do ReleaseDetail
  // (componente condicional) — se a reserva (estado REQUESTED) fosse feita mas o despacho
  // falhasse de forma transitória, e a pessoa trocasse de aba (ou a própria reentrada
  // automática trocasse por ela, ao descobrir o run reservado), o ReleaseDetail desmontava e a
  // chave se perdia: uma nova tentativa usaria uma chave NOVA e seria recusada pelo gate de "só
  // um run ativo por instalação" (a reserva antiga ainda conta como ativa), sem nenhum caminho
  // de recuperação nesta tela. Em vez de guardar a identidade do pedido em outro lugar, o
  // PRÓPRIO run já carrega plan_id e idempotency_key (campos reais da API) — repetir StartRun
  // com os dois exatamente como estão SEMPRE aciona o caminho de replay do servidor,
  // independente de qual componente criou a reserva originalmente.
  const retryDispatch = useMutation({
    mutationFn: () => platformRunsAPI.start(run.data!.plan_id, run.data!.idempotency_key),
    onSuccess: () => { showToast('Novo despacho solicitado.'); void run.refetch() },
    onError: (err) => showToast(describePlatformReleaseError(err), 'error'),
  })

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
        {r.state === 'REQUESTED' && (
          <div className="rounded-control border border-status-warning-border bg-status-warning-soft p-4">
            <p className="text-body-sm text-status-warning-strong">
              Esta atualização foi reservada, mas o despacho ao Update Agent ainda não foi confirmado (pode ter falhado de forma transitória).
            </p>
            {/* Achado Codex [P2] (12a rodada): StartRun resolve o replay por (operator_id,
                idempotency_key) — reusar a MESMA chave sob um operador DIFERENTE do que
                reservou o pedido nunca acha esse registro e cai no gate de "run ativo" em vez
                de retomar. Só quem reservou pode retomar por este caminho; outro operador
                precisa saber de quem é para pedir, ou (pendência) um caminho de recuperação
                que não dependa de quem fez o pedido original. */}
            {r.actor_id === currentUserId() ? (
              <Button className="mt-2" size="sm" variant="secondary" onClick={() => retryDispatch.mutate()} isLoading={retryDispatch.isPending}>
                Tentar despachar novamente
              </Button>
            ) : (
              <p className="mt-2 text-body-sm text-status-warning-strong">
                Só quem reservou esta atualização (operador {r.actor_id}) pode despachá-la de novo por aqui.
              </p>
            )}
          </div>
        )}
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
