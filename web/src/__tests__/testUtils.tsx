import { ReactElement } from 'react';
import { render } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes, RouterProvider, createMemoryRouter } from 'react-router-dom';
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

// Like renderAt, but inside a DATA router (as the real app runs), so useBlocker and the browser Back button can be exercised.
// `entries` is the history stack; the last one is the current page unless `index` says otherwise.
export function renderInDataRouter(ui: ReactElement, routePath: string, entries: string[], index = entries.length - 1) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = createMemoryRouter(
    [
      { path: routePath, element: ui },
      { path: '*', element: <div data-testid="elsewhere">elsewhere</div> },
    ],
    { initialEntries: entries, initialIndex: index },
  );
  const view = render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return { ...view, router };
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
  // the signed-in user (login stores the user object); fixtures that are "assigned to me" use this id
  localStorage.setItem('user', JSON.stringify({ id: 'u-1', name: 'Operador Teste', email: 'operador@example.com' }));
}
