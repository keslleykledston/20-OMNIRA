import type { ReactElement } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Outlet, Route, Routes } from 'react-router-dom'
import axios from 'axios'
import PlatformReleasesPage from '../pages/PlatformReleasesPage'
import { usePlatformOperator } from '../hooks/usePlatformOperator'
import type { PlatformInstallation, PlatformRelease, PlatformUpdateRun, PlatformUpdateEvent } from '../lib/platformRelease'
import { setSession } from './testUtils'

// AdminShell fetches the installation query ONCE and hands it to the page via Outlet context
// (never a second useQuery for the same key inside the page — a real-browser bug, see
// AdminShell/PlatformReleasesPage comments). This stub reproduces exactly that wiring, calling
// the SAME hook AdminShell calls, so these tests exercise the real contract the page depends on.
function FakeShell() {
  const platformOperator = usePlatformOperator()
  if (platformOperator.isLoading) return <div>Carregando…</div>
  return <Outlet context={platformOperator} />
}

function renderAt(ui: ReactElement, path = '/', routePath = '*') {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route element={<FakeShell />}>
            <Route path={routePath} element={ui} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

// Like renderAt, but hands back the QueryClient — to force a refetch of the first page
// deterministically (never waiting on the real 10s refetchInterval in a test).
function renderAtWithClient(ui: ReactElement, path = '/', routePath = '*') {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const view = render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route element={<FakeShell />}>
            <Route path={routePath} element={ui} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
  return { ...view, queryClient }
}

vi.mock('axios')
const unauthorized = vi.hoisted(() => vi.fn())
vi.mock('../lib/session', async (orig) => ({ ...(await orig<typeof import('../lib/session')>()), handleUnauthorized: unauthorized }))

const manifest = (releaseId = 'rel-2026.10.0') => ({
  schema_version: 1,
  release_id: releaseId,
  platform_version: '2026.10.0',
  channel: 'stable' as const,
  source: { repository: 'omnira/omnira', commit_sha: 'abc123', workflow_run_id: '42' },
  artifacts: [{ unit: 'api', image: 'ghcr.io/omnira/api:2026.10.0' }],
  modules_changed: ['platformrelease'],
  compatibility: { from_versions: ['2026.09.0'], min_agent_protocol: 1, database_strategy: 'expand_only' },
  migrations: { start_after: '000172', target: '000173', checksum_bundle: 'sha256:deadbeef' },
  rollout: { workloads: ['api'], preflight_policy: 'strict', recovery_class: 'manual' },
  approved_by: 'dono',
})

const release = (releaseId = 'rel-2026.10.0'): PlatformRelease => ({
  release_id: releaseId,
  channel: 'stable',
  platform_version: '2026.10.0',
  manifest: manifest(releaseId),
  ingested_by: 'op-1',
  ingested_at: '2026-10-20T10:00:00Z',
})

const installation = (version = '2026.09.0'): PlatformInstallation => ({
  channel: 'stable',
  agent_protocol: 1,
  current_version: version,
  current_release_id: 'rel-2026.09.0',
  current_digests: {},
  updated_at: '2026-10-20T09:00:00Z',
})

const run = (state: string, id = 'run-1'): PlatformUpdateRun => ({
  id,
  plan_id: 'plan-1',
  release_id: 'rel-2026.10.0',
  state,
  idempotency_key: 'key-1',
  actor_id: 'op-1',
  created_at: '2026-10-20T10:05:00Z',
  updated_at: '2026-10-20T10:05:00Z',
})

let releases: PlatformRelease[]
let runs: PlatformUpdateRun[]
let installationState: PlatformInstallation | { notFound: true }
// Keyed by run id — events are per-run on the real server; a flat shared array would let one
// run's events leak into another run's trail in these tests without that actually proving
// anything about the page's own cursor/accumulator reset logic.
let eventsByRun: Record<string, PlatformUpdateEvent[]>

function serve() {
  vi.mocked(axios.get).mockImplementation(async (url: string, config?: any) => {
    const u = String(url)
    if (u.endsWith('/platform/installation')) {
      if ('notFound' in installationState) return Promise.reject({ response: { status: 404 } })
      return { data: installationState }
    }
    if (u.endsWith('/platform/releases')) return { data: { items: releases } }
    if (u.endsWith('/platform/updates/runs')) return { data: { items: runs } }
    const eventsMatch = u.match(/\/platform\/updates\/runs\/([^/]+)\/events$/)
    if (eventsMatch) {
      const after = (config?.params?.after as number | undefined) ?? -1
      const forRun = eventsByRun[eventsMatch[1]] ?? []
      return { data: { items: forRun.filter((e) => e.seq > after) } }
    }
    if (/\/platform\/updates\/runs\/[^/]+$/.test(u)) {
      const id = u.split('/').pop()!
      const found = runs.find((r) => r.id === id)
      if (!found) return Promise.reject({ response: { status: 404 } })
      return { data: found }
    }
    return Promise.reject({ response: { status: 404 } })
  })
}

beforeEach(() => {
  vi.resetAllMocks()
  unauthorized.mockReset()
  localStorage.clear()
  setSession()
  releases = [release()]
  runs = []
  installationState = installation()
  eventsByRun = {}
  serve()
})

describe('PlatformReleasesPage — permission', () => {
  it('shows a permission message (never the screen) for whoever is not a platform operator, and asks for nothing else', async () => {
    installationState = { notFound: true }
    renderAt(<PlatformReleasesPage />)
    expect(await screen.findByText(/Esta área é só para operadores de plataforma/)).toBeInTheDocument()
    expect(vi.mocked(axios.get).mock.calls.some((c) => String(c[0]).includes('/platform/releases'))).toBe(false)
    expect(vi.mocked(axios.get).mock.calls.some((c) => String(c[0]).includes('/platform/updates/runs'))).toBe(false)
  })
})

describe('PlatformReleasesPage — golden path', () => {
  it('shows the current installation and the catalog', async () => {
    renderAt(<PlatformReleasesPage />)
    expect(await screen.findByText('2026.09.0')).toBeInTheDocument()
    expect(await screen.findByText('rel-2026.10.0')).toBeInTheDocument()
  })

  it('runs preflight (PASS) and starts an update after confirmation, scoped to the whole installation', async () => {
    const user = userEvent.setup()
    vi.mocked(axios.post).mockImplementation(async (url: string, body: unknown) => {
      const u = String(url)
      if (u.endsWith('/platform/updates/plans')) {
        return {
          data: {
            id: 'plan-1', release_id: 'rel-2026.10.0', preflight_status: 'PASS',
            preflight_detail: { version_known: true, version_compatible: true, agent_protocol_ok: true, reasons: [] },
            created_by: 'op-1', created_at: '2026-10-20T10:04:00Z', expires_at: '2026-10-20T10:19:00Z',
          },
        }
      }
      if (u.endsWith('/platform/updates/runs')) {
        expect((body as { plan_id: string }).plan_id).toBe('plan-1')
        const created = run('LOCKED')
        runs = [...runs, created]
        return { data: created }
      }
      throw new Error(`unexpected POST ${u}`)
    })

    renderAt(<PlatformReleasesPage />)
    await user.click(await screen.findByRole('button', { name: 'Detalhes' }))
    await user.click(screen.getByRole('button', { name: 'Rodar preflight' }))
    expect(await screen.findByText('PASS')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Iniciar atualização (escopo global)' }))
    const dialog = await screen.findByRole('dialog', { name: 'Confirmar atualização de escopo global' })
    expect(within(dialog).getByText(/atualiza a instalação OMNIRA inteira/)).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Iniciar atualização' }))

    await waitFor(() => expect(vi.mocked(axios.post).mock.calls.some((c) => String(c[0]).endsWith('/platform/updates/runs'))).toBe(true))
    expect(await screen.findByText('Acompanhando run-1')).toBeInTheDocument()
  })

  it('shows BLOCK and its reasons, and never offers the start button for a blocked plan', async () => {
    const user = userEvent.setup()
    vi.mocked(axios.post).mockResolvedValue({
      data: {
        id: 'plan-1', release_id: 'rel-2026.10.0', preflight_status: 'BLOCK',
        preflight_detail: { version_known: true, version_compatible: false, agent_protocol_ok: true, reasons: ['compatibility.from_versions não inclui a versão instalada (2026.09.0)'] },
        created_by: 'op-1', created_at: '2026-10-20T10:04:00Z', expires_at: '2026-10-20T10:19:00Z',
      },
    })
    renderAt(<PlatformReleasesPage />)
    await user.click(await screen.findByRole('button', { name: 'Detalhes' }))
    await user.click(screen.getByRole('button', { name: 'Rodar preflight' }))
    expect(await screen.findByText('BLOCK')).toBeInTheDocument()
    expect(screen.getByText(/compatibility.from_versions não inclui/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Iniciar atualização (escopo global)' })).toBeNull()
  })

  it('reuses the SAME Idempotency-Key when retrying StartRun after a transient failure (Codex round 9)', async () => {
    const user = userEvent.setup()
    const startKeys: string[] = []
    let failStart = true
    vi.mocked(axios.post).mockImplementation(async (url: string, _body: unknown, config?: any) => {
      const u = String(url)
      if (u.endsWith('/platform/updates/plans')) {
        return {
          data: {
            id: 'plan-1', release_id: 'rel-2026.10.0', preflight_status: 'PASS',
            preflight_detail: { version_known: true, version_compatible: true, agent_protocol_ok: true, reasons: [] },
            created_by: 'op-1', created_at: '2026-10-20T10:04:00Z', expires_at: '2026-10-20T10:19:00Z',
          },
        }
      }
      if (u.endsWith('/platform/updates/runs')) {
        startKeys.push(config?.headers?.['Idempotency-Key'])
        if (failStart) { failStart = false; return Promise.reject({ response: { status: 503, data: 'tente novamente' } }) }
        return { data: run('LOCKED') }
      }
      throw new Error(`unexpected POST ${u}`)
    })

    renderAt(<PlatformReleasesPage />)
    await user.click(await screen.findByRole('button', { name: 'Detalhes' }))
    await user.click(screen.getByRole('button', { name: 'Rodar preflight' }))
    await screen.findByText('PASS')

    await user.click(screen.getByRole('button', { name: 'Iniciar atualização (escopo global)' }))
    let dialog = await screen.findByRole('dialog', { name: 'Confirmar atualização de escopo global' })
    await user.click(within(dialog).getByRole('button', { name: 'Iniciar atualização' }))
    await waitFor(() => expect(startKeys.length).toBe(1)) // first attempt failed

    // Retry: open the confirmation again and confirm — same logical request.
    await user.click(screen.getByRole('button', { name: 'Iniciar atualização (escopo global)' }))
    dialog = await screen.findByRole('dialog', { name: 'Confirmar atualização de escopo global' })
    await user.click(within(dialog).getByRole('button', { name: 'Iniciar atualização' }))
    await waitFor(() => expect(startKeys.length).toBe(2))

    expect(startKeys[0]).toBe(startKeys[1])
    expect(startKeys[0]).toBeTruthy()
  })

  it('generates a NEW Idempotency-Key for StartRun when the submitted plan changed after a failed attempt (Codex round 10)', async () => {
    const user = userEvent.setup()
    const startKeysByPlan: Record<string, string> = {}
    let planCount = 0
    vi.mocked(axios.post).mockImplementation(async (url: string, _body: unknown, config?: any) => {
      const u = String(url)
      if (u.endsWith('/platform/updates/plans')) {
        planCount += 1
        return {
          data: {
            id: `plan-${planCount}`, release_id: 'rel-2026.10.0', preflight_status: 'PASS',
            preflight_detail: { version_known: true, version_compatible: true, agent_protocol_ok: true, reasons: [] },
            created_by: 'op-1', created_at: '2026-10-20T10:04:00Z', expires_at: '2026-10-20T10:19:00Z',
          },
        }
      }
      if (u.endsWith('/platform/updates/runs')) {
        const body = _body as { plan_id: string }
        startKeysByPlan[body.plan_id] = config?.headers?.['Idempotency-Key']
        // plan-1's start always fails (e.g. the plan went stale); plan-2 succeeds.
        if (body.plan_id === 'plan-1') return Promise.reject({ response: { status: 422, data: 'plano expirado' } })
        return { data: run('LOCKED') }
      }
      throw new Error(`unexpected POST ${u}`)
    })

    renderAt(<PlatformReleasesPage />)
    await user.click(await screen.findByRole('button', { name: 'Detalhes' }))
    await user.click(screen.getByRole('button', { name: 'Rodar preflight' }))
    await screen.findByText('PASS')
    await user.click(screen.getByRole('button', { name: 'Iniciar atualização (escopo global)' }))
    let dialog = await screen.findByRole('dialog', { name: 'Confirmar atualização de escopo global' })
    await user.click(within(dialog).getByRole('button', { name: 'Iniciar atualização' }))
    await waitFor(() => expect(startKeysByPlan['plan-1']).toBeTruthy())

    // Re-run preflight: a NEW plan (plan-2) replaces the stale one in the UI.
    await user.click(screen.getByRole('button', { name: 'Rodar preflight' }))
    await screen.findByText('PASS')
    await user.click(screen.getByRole('button', { name: 'Iniciar atualização (escopo global)' }))
    dialog = await screen.findByRole('dialog', { name: 'Confirmar atualização de escopo global' })
    await user.click(within(dialog).getByRole('button', { name: 'Iniciar atualização' }))
    await waitFor(() => expect(startKeysByPlan['plan-2']).toBeTruthy())

    // Different plans must never share a key — reusing plan-1's key for plan-2 would hit the
    // server's ErrKeyMismatch (the stored request_hash includes the plan_id).
    expect(startKeysByPlan['plan-1']).not.toBe(startKeysByPlan['plan-2'])
  })
})

describe('PlatformReleasesPage — reentry with a run in progress', () => {
  it('reopening the screen while a run is active resumes tracking it, not the catalog', async () => {
    runs = [run('DEPLOYING')]
    eventsByRun = { 'run-1': [{ seq: 0, kind: 'run.locked', detail: {}, created_at: '2026-10-20T10:05:00Z' }] }
    renderAt(<PlatformReleasesPage />)
    expect(await screen.findByText('Acompanhando run-1')).toBeInTheDocument()
    expect(screen.getAllByText(runLabelOf('DEPLOYING')).length).toBeGreaterThan(0)
  })

  it('a terminal run is shown in history but does not force the tracking view open on its own', async () => {
    runs = [run('SUCCEEDED')]
    renderAt(<PlatformReleasesPage />)
    await screen.findByText('rel-2026.10.0')
    expect(screen.queryByText('Acompanhando run-1')).toBeNull()
  })

  it('a FAILED run is NOT terminal (FAILED -> RECOVERING -> ROLLED_BACK/RECOVERY_REQUIRED is legal): reentry still resumes tracking it', async () => {
    runs = [run('FAILED')]
    renderAt(<PlatformReleasesPage />)
    expect(await screen.findByText('Acompanhando run-1')).toBeInTheDocument()
  })
})

describe('PlatformReleasesPage — closing tracking sticks (Codex round 2)', () => {
  it('closing the tracking view while the run is still active does not reopen it on its own', async () => {
    const user = userEvent.setup()
    runs = [run('DEPLOYING')]
    eventsByRun = { 'run-1': [{ seq: 0, kind: 'run.locked', detail: {}, created_at: '2026-10-20T10:05:00Z' }] }
    renderAt(<PlatformReleasesPage />)
    await screen.findByText('Acompanhando run-1')
    await user.click(screen.getByRole('button', { name: 'Fechar acompanhamento' }))
    // The catalog (or at least "not the tracking card") stays closed — reentry only ever fires once.
    expect(screen.queryByText('Acompanhando run-1')).toBeNull()
    // give any stray effect a tick to (wrongly) reopen it
    await new Promise((r) => setTimeout(r, 10))
    expect(screen.queryByText('Acompanhando run-1')).toBeNull()
  })

  it('closing tracking also sticks for a run STARTED during this visit (no active run existed on load, so reentry\'s own ref was never set — Codex round 3)', async () => {
    const user = userEvent.setup()
    // No active run on load: the auto-resume effect never fires, so its ref alone cannot be
    // what prevents the reopen — trackRun must mark it on every path that selects a run.
    runs = []
    vi.mocked(axios.post).mockImplementation(async (url: string) => {
      if (String(url).endsWith('/platform/updates/plans')) {
        return {
          data: {
            id: 'plan-1', release_id: 'rel-2026.10.0', preflight_status: 'PASS',
            preflight_detail: { version_known: true, version_compatible: true, agent_protocol_ok: true, reasons: [] },
            created_by: 'op-1', created_at: '2026-10-20T10:04:00Z', expires_at: '2026-10-20T10:19:00Z',
          },
        }
      }
      if (String(url).endsWith('/platform/updates/runs')) {
        const created = run('LOCKED')
        runs = [created]
        eventsByRun = { 'run-1': [{ seq: 0, kind: 'run.locked', detail: {}, created_at: '2026-10-20T10:05:00Z' }] }
        return { data: created }
      }
      throw new Error(`unexpected POST ${url}`)
    })
    renderAt(<PlatformReleasesPage />)
    await user.click(await screen.findByRole('button', { name: 'Detalhes' }))
    await user.click(screen.getByRole('button', { name: 'Rodar preflight' }))
    await screen.findByText('PASS')
    await user.click(screen.getByRole('button', { name: 'Iniciar atualização (escopo global)' }))
    const dialog = await screen.findByRole('dialog', { name: 'Confirmar atualização de escopo global' })
    await user.click(within(dialog).getByRole('button', { name: 'Iniciar atualização' }))
    await screen.findByText('Acompanhando run-1')

    await user.click(screen.getByRole('button', { name: 'Fechar acompanhamento' }))
    expect(screen.queryByText('Acompanhando run-1')).toBeNull()
    await new Promise((r) => setTimeout(r, 10))
    expect(screen.queryByText('Acompanhando run-1')).toBeNull()
  })
})

describe('PlatformReleasesPage — terminal transition (Codex round 2)', () => {
  it('fetches events once more on the terminal transition, closing the race where the final checkpoint arrives between the last poll and the run finishing', async () => {
    // Tracking a run that is ALREADY terminal on the very first render makes `terminal` true
    // from render 1 — the SAME condition a live DEPLOYING -> SUCCEEDED transition produces, but
    // deterministic (no fake timers needed): the fix's effect must still fire its one extra
    // events fetch instead of relying only on the initial query.
    runs = [run('SUCCEEDED')]
    eventsByRun = { 'run-1': [{ seq: 0, kind: 'run.locked', detail: {}, created_at: '2026-10-20T10:05:00Z' }] }
    let eventsCalls = 0
    vi.mocked(axios.get).mockImplementation(async (url: string, config?: any) => {
      const u = String(url)
      if (u.endsWith('/platform/installation')) return { data: installation() }
      if (u.endsWith('/platform/releases')) return { data: { items: releases } }
      if (u.endsWith('/platform/updates/runs')) return { data: { items: runs } }
      if (u.endsWith('/run-1') && !u.includes('/events')) return { data: run('SUCCEEDED') }
      if (u.endsWith('/events')) {
        eventsCalls += 1
        const after = (config?.params?.after as number | undefined) ?? -1
        return { data: { items: (eventsByRun['run-1'] ?? []).filter((e) => e.seq > after) } }
      }
      return Promise.reject({ response: { status: 404 } })
    })
    renderAt(<PlatformReleasesPage />)
    const user = userEvent.setup()
    await screen.findByText('rel-2026.10.0')
    await user.click(screen.getByRole('tab', { name: 'Atualizações' }))
    await user.click(await screen.findByRole('button', { name: 'Acompanhar' }))
    await screen.findByText('Acompanhando run-1')
    // The initial query fetch (1) plus the terminal-transition effect's extra fetch (2).
    await waitFor(() => expect(eventsCalls).toBeGreaterThanOrEqual(2))
  })

  it('invalidates the installation data when a tracked run succeeds', async () => {
    runs = [run('SUCCEEDED')]
    eventsByRun = { 'run-1': [{ seq: 0, kind: 'run.locked', detail: {}, created_at: '2026-10-20T10:05:00Z' }] }
    let installCalls = 0
    vi.mocked(axios.get).mockImplementation(async (url: string, config?: any) => {
      const u = String(url)
      if (u.endsWith('/platform/installation')) {
        installCalls += 1
        return { data: installation(installCalls === 1 ? '2026.09.0' : '2026.10.0') }
      }
      if (u.endsWith('/platform/releases')) return { data: { items: releases } }
      if (u.endsWith('/platform/updates/runs')) return { data: { items: runs } }
      if (u.endsWith('/run-1')) return { data: run('SUCCEEDED') }
      const eventsMatch = u.match(/\/platform\/updates\/runs\/([^/]+)\/events$/)
      if (eventsMatch) {
        const after = (config?.params?.after as number | undefined) ?? -1
        return { data: { items: (eventsByRun[eventsMatch[1]] ?? []).filter((e) => e.seq > after) } }
      }
      return Promise.reject({ response: { status: 404 } })
    })
    renderAt(<PlatformReleasesPage />)
    // A SUCCEEDED run is terminal, so reentry does not auto-open it — reach it from its history
    // row instead (the "Atualizações" tab), which exercises the same terminal-transition effect.
    await screen.findByText('rel-2026.10.0')
    const user = userEvent.setup()
    await user.click(screen.getByRole('tab', { name: 'Atualizações' }))
    await user.click(await screen.findByRole('button', { name: 'Acompanhar' }))
    await screen.findByText('Acompanhando run-1')
    await waitFor(() => expect(installCalls).toBeGreaterThanOrEqual(2))
  })
})

describe('PlatformReleasesPage — checkpoint detail and event-fetch errors (Codex round 4)', () => {
  it('renders the checkpoint step, status and diagnostic message — never just "checkpoint" and a time', async () => {
    runs = [run('DEPLOYING')]
    eventsByRun = {
      'run-1': [
        { seq: 0, kind: 'run.locked', detail: {}, created_at: '2026-10-20T10:05:00Z' },
        { seq: 1, kind: 'checkpoint', detail: { step: 'migrations_expand', status: 'failed', detail: 'migration 000173 timed out' }, created_at: '2026-10-20T10:06:00Z' },
      ],
    }
    renderAt(<PlatformReleasesPage />)
    await screen.findByText('Acompanhando run-1')
    expect(await screen.findByText('migrations_expand')).toBeInTheDocument()
    expect(screen.getByText('falhou')).toBeInTheDocument()
    expect(screen.getByText('migration 000173 timed out')).toBeInTheDocument()
    // the raw "checkpoint" kind is never shown on its own for an actual checkpoint event
    expect(screen.queryByText('checkpoint', { selector: 'span.font-medium' })).toBeNull()
  })

  it('surfaces an event-fetch failure with a retry action, instead of silently showing an empty or stale trail', async () => {
    runs = [run('DEPLOYING')]
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      const u = String(url)
      if (u.endsWith('/platform/installation')) return { data: installation() }
      if (u.endsWith('/platform/releases')) return { data: { items: releases } }
      if (u.endsWith('/platform/updates/runs')) return { data: { items: runs } }
      if (u.endsWith('/run-1') && !u.includes('/events')) return { data: run('DEPLOYING') }
      if (u.endsWith('/events')) return Promise.reject({ response: { status: 500, data: 'internal server error' } })
      return Promise.reject({ response: { status: 404 } })
    })
    renderAt(<PlatformReleasesPage />)
    await screen.findByText('Acompanhando run-1')
    expect(await screen.findByText(/Não foi possível buscar os eventos mais recentes/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Tentar novamente' })).toBeInTheDocument()
  })
})

describe('PlatformReleasesPage — pagination (Codex round 5)', () => {
  it('"Carregar mais releases" fetches the next page by the before cursor and appends it, never replacing the first page', async () => {
    const firstPage = Array.from({ length: 50 }, (_, i) => release(`rel-first-${i}`))
    const secondPage = [release('rel-older-1')]
    releases = firstPage
    let lastBefore: string | undefined
    vi.mocked(axios.get).mockImplementation(async (url: string, config?: any) => {
      const u = String(url)
      if (u.endsWith('/platform/installation')) return { data: installation() }
      if (u.endsWith('/platform/releases')) {
        const before = config?.params?.before as string | undefined
        lastBefore = before
        return { data: { items: before ? secondPage : firstPage } }
      }
      if (u.endsWith('/platform/updates/runs')) return { data: { items: [] } }
      return Promise.reject({ response: { status: 404 } })
    })
    renderAt(<PlatformReleasesPage />)
    await screen.findByText('rel-first-0')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Carregar mais releases' }))
    expect(await screen.findByText('rel-older-1')).toBeInTheDocument()
    // the cursor sent is the LAST item of the page already shown (ingested_at), never undefined again
    expect(lastBefore).toBe(firstPage[firstPage.length - 1].ingested_at)
    expect(screen.getByText('rel-first-0')).toBeInTheDocument()
    // the second page had fewer than 50 items: no further "load more" is offered
    expect(screen.queryByRole('button', { name: 'Carregar mais releases' })).toBeNull()
  })

  it('"Carregar mais execuções" fetches the next page of run history by the before cursor', async () => {
    const firstPage = Array.from({ length: 50 }, (_, i) => run('SUCCEEDED', `run-first-${i}`))
    const secondPage = [run('SUCCEEDED', 'run-older-1')]
    runs = firstPage
    vi.mocked(axios.get).mockImplementation(async (url: string, config?: any) => {
      const u = String(url)
      if (u.endsWith('/platform/installation')) return { data: installation() }
      if (u.endsWith('/platform/releases')) return { data: { items: [] } }
      if (u.endsWith('/platform/updates/runs')) {
        const before = config?.params?.before as string | undefined
        return { data: { items: before ? secondPage : firstPage } }
      }
      return Promise.reject({ response: { status: 404 } })
    })
    renderAt(<PlatformReleasesPage />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('tab', { name: 'Atualizações' }))
    await screen.findByText('run-first-0')
    await user.click(screen.getByRole('button', { name: 'Carregar mais execuções' }))
    expect(await screen.findByText('run-older-1')).toBeInTheDocument()
    expect(screen.getByText('run-first-0')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Carregar mais execuções' })).toBeNull()
  })

  it('resets the accumulated older runs (never leaves a silent gap) when a new run shifts the first page\'s boundary', async () => {
    const firstPage = Array.from({ length: 50 }, (_, i) => run('SUCCEEDED', `run-first-${i}`))
    const secondPage = [run('SUCCEEDED', 'run-older-1')]
    let currentFirstPage = firstPage
    vi.mocked(axios.get).mockImplementation(async (url: string, config?: any) => {
      const u = String(url)
      if (u.endsWith('/platform/installation')) return { data: installation() }
      if (u.endsWith('/platform/releases')) return { data: { items: [] } }
      if (u.endsWith('/platform/updates/runs')) {
        const before = config?.params?.before as string | undefined
        return { data: { items: before ? secondPage : currentFirstPage } }
      }
      return Promise.reject({ response: { status: 404 } })
    })
    const { queryClient } = renderAtWithClient(<PlatformReleasesPage />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('tab', { name: 'Atualizações' }))
    await screen.findByText('run-first-0')
    await user.click(screen.getByRole('button', { name: 'Carregar mais execuções' }))
    expect(await screen.findByText('run-older-1')).toBeInTheDocument()

    // A NEW run appears at the top of the first page (e.g. just started, or synced by the
    // 10s poll) — the item that used to be first-page's last entry is pushed into the gap
    // between the (refreshed) first page and the already-fetched second page.
    currentFirstPage = [run('DEPLOYING', 'run-new'), ...firstPage.slice(0, 49)]
    await queryClient.invalidateQueries({ queryKey: ['platform-runs'] })

    await screen.findByText('run-new')
    // The stale second page is discarded rather than silently hiding the gap — the button
    // comes back so the person can re-fetch what is actually after the (new) first page.
    await waitFor(() => expect(screen.queryByText('run-older-1')).toBeNull())
    expect(await screen.findByRole('button', { name: 'Carregar mais execuções' })).toBeInTheDocument()
  })

  it('discards a "load more" response for the OLD first-page boundary that resolves after a reset (Codex round 7)', async () => {
    const firstPage = Array.from({ length: 50 }, (_, i) => run('SUCCEEDED', `run-first-${i}`))
    const staleSecondPage = [run('SUCCEEDED', 'run-stale')]
    let currentFirstPage = firstPage
    let resolveSecondPage: (() => void) | undefined
    vi.mocked(axios.get).mockImplementation((url: string, config?: any) => {
      const u = String(url)
      if (u.endsWith('/platform/installation')) return Promise.resolve({ data: installation() })
      if (u.endsWith('/platform/releases')) return Promise.resolve({ data: { items: [] } })
      if (u.endsWith('/platform/updates/runs')) {
        const before = config?.params?.before as string | undefined
        if (!before) return Promise.resolve({ data: { items: currentFirstPage } })
        // The "load more" request for the OLD boundary hangs until the test explicitly
        // resolves it — AFTER the first-page refresh (and its reset) has already happened.
        return new Promise((resolve) => { resolveSecondPage = () => resolve({ data: { items: staleSecondPage } }) })
      }
      return Promise.reject({ response: { status: 404 } })
    })
    const { queryClient } = renderAtWithClient(<PlatformReleasesPage />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('tab', { name: 'Atualizações' }))
    await screen.findByText('run-first-0')
    await user.click(screen.getByRole('button', { name: 'Carregar mais execuções' }))
    await waitFor(() => expect(resolveSecondPage).toBeDefined())

    // The first page refreshes (bumping the generation) WHILE that request is still in flight.
    currentFirstPage = [run('DEPLOYING', 'run-new'), ...firstPage.slice(0, 49)]
    await queryClient.invalidateQueries({ queryKey: ['platform-runs'] })
    await screen.findByText('run-new')

    // Only NOW does the stale, old-boundary response arrive.
    await act(async () => {
      resolveSecondPage!()
      await new Promise((r) => setTimeout(r, 20))
    })
    // It must never reappear: onSuccess compares its own generation against the current one.
    expect(screen.queryByText('run-stale')).toBeNull()
    expect(screen.getByRole('button', { name: 'Carregar mais execuções' })).toBeInTheDocument()
  })
})

describe('PlatformReleasesPage — switching selection does not leak stale state (Codex round 1)', () => {
  it('selecting a different release resets the previous one\'s preflight plan', async () => {
    const user = userEvent.setup()
    releases = [release('rel-A'), release('rel-B')]
    vi.mocked(axios.post).mockImplementation(async (url: string, body: unknown) => {
      if (String(url).endsWith('/platform/updates/plans')) {
        const releaseId = (body as { release_id: string }).release_id
        return {
          data: {
            id: `plan-${releaseId}`, release_id: releaseId, preflight_status: 'PASS',
            preflight_detail: { version_known: true, version_compatible: true, agent_protocol_ok: true, reasons: [] },
            created_by: 'op-1', created_at: '2026-10-20T10:04:00Z', expires_at: '2026-10-20T10:19:00Z',
          },
        }
      }
      throw new Error(`unexpected POST ${url}`)
    })
    renderAt(<PlatformReleasesPage />)
    const rows = await screen.findAllByRole('button', { name: 'Detalhes' })
    await user.click(rows[0])
    await user.click(screen.getByRole('button', { name: 'Rodar preflight' }))
    await screen.findByText('PASS')
    await user.click(screen.getByRole('button', { name: 'Fechar' }))

    // Selecting the OTHER release must start with no plan shown at all — never rel-A's stale PASS.
    const rowsAgain = await screen.findAllByRole('button', { name: 'Detalhes' })
    await user.click(rowsAgain[1])
    expect(screen.queryByText('PASS')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Iniciar atualização (escopo global)' })).toBeNull()
  })

  it('tracking a different run resets the previous run\'s accumulated event trail and cursor', async () => {
    runs = [run('DEPLOYING', 'run-1'), run('DEPLOYING', 'run-2')]
    eventsByRun = {
      // run-1 has 2 events, the second only visible via its own distinct timestamp.
      'run-1': [
        { seq: 0, kind: 'run.locked', detail: {}, created_at: '2026-10-20T10:05:00Z' },
        { seq: 1, kind: 'checkpoint', detail: {}, created_at: '2026-10-20T10:06:00Z' },
      ],
      // run-2 has only 1 event, so a leaked run-1 cursor (after=1) would wrongly skip it, and a
      // leaked accumulator would wrongly still show run-1's 10:06:00 entry alongside it.
      'run-2': [{ seq: 0, kind: 'run.locked', detail: {}, created_at: '2026-10-20T10:07:00Z' }],
    }
    renderAt(<PlatformReleasesPage />)
    // run-1 is active, so reentry auto-tracks it first; switch to its history row for run-2.
    await screen.findByText('Acompanhando run-1')
    expect(await screen.findByText('20/10/2026, 10:06:00')).toBeInTheDocument()

    const user = userEvent.setup()
    const trackButtons = await screen.findAllByRole('button', { name: 'Acompanhar' })
    // Only run-2's row offers a fresh "Acompanhar" button (run-1 is already the one being tracked).
    await user.click(trackButtons[trackButtons.length - 1]);
    expect(await screen.findByText('Acompanhando run-2')).toBeInTheDocument()
    expect(await screen.findByText('20/10/2026, 10:07:00')).toBeInTheDocument()
    // run-1's second event must never leak into (nor be skipped from) run-2's trail.
    expect(screen.queryByText('20/10/2026, 10:06:00')).toBeNull()
  })
})

function runLabelOf(state: string): string {
  const labels: Record<string, string> = { DEPLOYING: 'Implantando' }
  return labels[state] ?? state
}
