import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import ReportsPage from '../pages/Reports';
import { renderAt, setSession } from './testUtils';

// FRONTEND.1: mock-backed pages must never present fabricated business data
// as if it were real outside development. The page here has no real
// backend behind it (see docs/delivery/HANDOFF-NEXT-AGENT.md DESIGN.5).
// Supervisor became real in PRODUCT.1, Tickets in PRODUCT.2-B, Dashboard in
// PRODUCT.3-B and Accounts (Empresas) in ADR-0018 — none of them is contained anymore; see
// SupervisorDashboard.test.tsx / Tickets.test.tsx / Dashboard.test.tsx.

const MOCK_SURFACES: Array<{ title: string; Component: () => JSX.Element }> = [
  { title: 'Relatórios', Component: ReportsPage },
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
      expect(screen.queryByText(/Integração API com certificado SSL/)).not.toBeInTheDocument();
      expect(screen.queryByText(/Conformidade SLA/)).not.toBeInTheDocument();
    });
  }
});
