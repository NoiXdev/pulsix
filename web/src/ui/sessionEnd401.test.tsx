import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider, useQuery } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from './toast.tsx';
import { ApiError } from '../api/client.ts';
import { useQueryGuard } from './useQueryGuard.tsx';
import { clearRenewed, markRenewed } from '../lib/sessionEnd.ts';

function Harness({ queryFn }: { queryFn: () => Promise<{ value: string }> }) {
  const query = useQuery({ queryKey: ['thing'], queryFn, retry: false });
  const guard = useQueryGuard(query);
  if (guard) return guard;
  return <div>DATA: {query.data?.value ?? 'none'}</div>;
}

function renderIt(qc: QueryClient, queryFn: () => Promise<{ value: string }>) {
  const rootRoute = createRootRoute();
  const login = createRoute({
    getParentRoute: () => rootRoute,
    path: '/login',
    validateSearch: (s: Record<string, unknown>) => ({
      step: typeof s.step === 'string' ? s.step : undefined,
      notice: typeof s.notice === 'string' ? s.notice : undefined,
    }),
    component: () => <div>LOGIN PAGE</div>,
  });
  const page = createRoute({ getParentRoute: () => rootRoute, path: '/test', component: () => <Harness queryFn={queryFn} /> });
  const router = createRouter({ routeTree: rootRoute.addChildren([login, page]), history: createMemoryHistory({ initialEntries: ['/test'] }) });
  render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
  return router;
}

const unauthorized = () => new ApiError(401, { detail: 'unauthorized' });

afterEach(() => clearRenewed());

describe('a 401 that sends the person to /login (backlog 3.93)', () => {
  it('carries the session_expired notice when the cached session end has passed', async () => {
    const qc = new QueryClient();
    qc.setQueryData(['me'], { session_expires_at: new Date(Date.now() - 5000).toISOString() });
    const router = renderIt(qc, () => Promise.reject(unauthorized()));

    expect(await screen.findByText('LOGIN PAGE')).toBeInTheDocument();
    expect(router.state.location.search).toMatchObject({ notice: 'session_expired' });
  });

  it('has no notice when the session had time left', async () => {
    const qc = new QueryClient();
    qc.setQueryData(['me'], { session_expires_at: new Date(Date.now() + 600_000).toISOString() });
    const router = renderIt(qc, () => Promise.reject(unauthorized()));

    expect(await screen.findByText('LOGIN PAGE')).toBeInTheDocument();
    expect(router.state.location.search.notice).toBeUndefined();
  });

  it('retries a 401 once right after a renewal instead of redirecting', async () => {
    markRenewed();
    const queryFn = vi.fn().mockRejectedValueOnce(unauthorized()).mockResolvedValue({ value: 'fresh' });
    const router = renderIt(new QueryClient(), queryFn);

    expect(await screen.findByText('DATA: fresh')).toBeInTheDocument();
    expect(queryFn).toHaveBeenCalledTimes(2);
    expect(router.state.location.pathname).toBe('/test');
  });

  it('redirects when the retry is a 401 too', async () => {
    markRenewed();
    const queryFn = vi.fn().mockRejectedValue(unauthorized());
    const router = renderIt(new QueryClient(), queryFn);

    expect(await screen.findByText('LOGIN PAGE')).toBeInTheDocument();
    expect(queryFn).toHaveBeenCalledTimes(2);
    expect(router.state.location.pathname).toBe('/login');
  });

  it('redirects at once without a recent renewal', async () => {
    const queryFn = vi.fn().mockRejectedValue(unauthorized());
    renderIt(new QueryClient(), queryFn);

    await waitFor(() => expect(screen.getByText('LOGIN PAGE')).toBeInTheDocument());
    expect(queryFn).toHaveBeenCalledTimes(1);
  });
});
