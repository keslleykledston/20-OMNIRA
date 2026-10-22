import type { ReactElement } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
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
let events: PlatformUpdateEvent[]

function serve() {
  vi.mocked(axios.get).mockImplementation(async (url: string, config?: any) => {
    const u = String(url)
    if (u.endsWith('/platform/installation')) {
      if ('notFound' in installationState) return Promise.reject({ response: { status: 404 } })
      return { data: installationState }
    }
    if (u.endsWith('/platform/releases')) return { data: { items: releases } }
    if (u.endsWith('/platform/updates/runs')) return { data: { items: runs } }
    if (/\/platform\/updates\/runs\/[^/]+$/.test(u)) {
      const id = u.split('/').pop()!
      const found = runs.find((r) => r.id === id)
      if (!found) return Promise.reject({ response: { status: 404 } })
      return { data: found }
    }
    if (u.endsWith('/events')) {
      const after = (config?.params?.after as number | undefined) ?? -1
      return { data: { items: events.filter((e) => e.seq > after) } }
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
  events = []
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
})

describe('PlatformReleasesPage — reentry with a run in progress', () => {
  it('reopening the screen while a run is active resumes tracking it, not the catalog', async () => {
    runs = [run('DEPLOYING')]
    events = [{ seq: 0, kind: 'run.locked', detail: {}, created_at: '2026-10-20T10:05:00Z' }]
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
})

function runLabelOf(state: string): string {
  const labels: Record<string, string> = { DEPLOYING: 'Implantando' }
  return labels[state] ?? state
}
