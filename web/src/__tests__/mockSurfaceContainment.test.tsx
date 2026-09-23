import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import Dashboard from '../pages/Dashboard';
import ReportsPage from '../pages/Reports';
import Accounts from '../pages/Accounts';
import { renderAt, setSession } from './testUtils';

// FRONTEND.1: mock-backed pages must never present fabricated business data
// as if it were real outside development. The three pages here have no real
// backend behind them (see docs/delivery/HANDOFF-NEXT-AGENT.md DESIGN.5).
// Supervisor became real in PRODUCT.1, Tickets in PRODUCT.2-B — neither is
// contained anymore; see SupervisorDashboard.test.tsx / Tickets.test.tsx.

const MOCK_SURFACES: Array<{ title: string; Component: () => JSX.Element }> = [
  { title: 'Dashboard', Component: Dashboard },
  { title: 'Relatórios', Component: ReportsPage },
  { title: 'Contas', Component: Accounts },
];

afterEach(() => {
  vi.unstubAllEnvs();
  vi.clearAllMocks();
});

describe('mock surface containment', () => {
  for (const { title, Component } of MOCK_SURFACES) {
    it(`${title}: shows the unavailable state — never fixture data — outside development`, () => {
      vi.stubEnv('DEV', false);
      setSession();
      renderAt(<Component />);

      expect(screen.getByRole('heading', { name: title })).toBeInTheDocument();
      expect(
        screen.getByText('Este recurso ainda não está disponível nesta implantação.'),
      ).toBeInTheDocument();

      // None of this fixture's known fake business data ever reaches the DOM.
      expect(screen.queryByText(/Test Company LTDA/)).not.toBeInTheDocument();
      expect(screen.queryByText(/Integração API com certificado SSL/)).not.toBeInTheDocument();
      expect(screen.queryByText(/Conformidade SLA/)).not.toBeInTheDocument();
    });
  }

  it('Dashboard: keeps its normal fixture-backed content in development (unchanged behavior)', async () => {
    // import.meta.env.DEV is true under the default Vitest ("test") mode —
    // exercising the real, un-stubbed default confirms dev behavior is intact.
    setSession();
    renderAt(<Dashboard />);
    expect(await screen.findByText('Aqui está o resumo do seu atendimento hoje.')).toBeInTheDocument();
  });
});
