import { ReactElement } from 'react';
import { render } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import axios from 'axios';
import { vi } from 'vitest';

export const TENANT = 'tenant-a-uuid';

// Renders inside a router + react-query (no retries) at the given path.
export function renderAt(ui: ReactElement, path = '/', routePath = '*') {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path={routePath} element={ui} />
          <Route path="*" element={<div data-testid="elsewhere">elsewhere</div>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  );
}

// Answers axios.get by URL suffix so query order never matters.
export function mockGets(routes: Record<string, unknown>) {
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    const key = Object.keys(routes).find((k) => url.endsWith(k));
    if (key === undefined) return Promise.reject({ response: { status: 404, data: 'not found' } });
    return { data: routes[key] };
  });
}

export function setSession() {
  localStorage.setItem('tenantId', TENANT);
  localStorage.setItem('token', 'tok');
}
