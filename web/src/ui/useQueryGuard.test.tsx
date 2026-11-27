import { StrictMode } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider, useInfiniteQuery, useQuery } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from './toast.tsx';
import { ApiError } from '../api/client.ts';
import { useQueryGuard } from './useQueryGuard.tsx';

function Harness({ queryFn }: { queryFn: () => Promise<{ value: string }> }) {
  const query = useQuery({ queryKey: ['thing'], queryFn, retry: false });
  const guard = useQueryGuard(query);
  if (guard) return guard;
  return <div>DATA: {query.data?.value ?? 'none'}</div>;
}

function renderHarness(qc: QueryClient, queryFn: () => Promise<{ value: string }>, strict = false) {
  const rootRoute = createRootRoute();
  const loginRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/login',
    validateSearch: (s: Record<string, unknown>) => ({ step: typeof s.step === 'string' ? s.step : undefined }),
    component: () => <div>LOGIN PAGE</div>,
  });
  const testRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/test',
    component: () => <Harness queryFn={queryFn} />,
  });
  const routeTree = rootRoute.addChildren([loginRoute, testRoute]);
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ['/test'] }) });

  const tree = (
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>
  );

  return render(strict ? <StrictMode>{tree}</StrictMode> : tree);
}

/**
 * Fix round 2 (task-15a-report.md): every wired-in page checked
 * `if (query.isError) return <QueryError .../>` before checking whether
 * data was already present. TanStack Query sets `isError` on ANY failed
 * fetch — including a background refetch — while leaving the previous
 * `data` in place, so that check tore a page down over a transient
 * hiccup exactly as often as over a genuine "never loaded" failure.
 * useQueryGuard (ui/useQueryGuard.tsx) is the one place that now makes
 * that distinction; these tests cover the case the previous round's own
 * tests never exercised (a query that loads, then fails on refetch).
 */
describe('useQueryGuard', () => {
  it('renders QueryError when the query has never had data (first-load failure, unchanged behaviour)', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    renderHarness(qc, () => Promise.reject(new ApiError(500, { detail: 'boom' })));

    expect(await screen.findByText('Something went wrong')).toBeInTheDocument();
    expect(screen.queryByText(/DATA:/)).not.toBeInTheDocument();
  });

  it('keeps rendering the loaded data and raises exactly one toast when a background refetch fails', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let call = 0;
    const queryFn = vi.fn(() => {
      call += 1;
      if (call === 1) return Promise.resolve({ value: 'loaded' });
      return Promise.reject(new ApiError(500, { detail: 'refetch broke' }));
    });

    // Rendered under StrictMode (as the real app is, in main.tsx): React
    // double-invokes effects on mount in development, which is exactly
    // the case the errorUpdatedAt dedup in useQueryGuard exists for — if
    // it were missing, this single failed refetch would raise two
    // toasts, not one.
    renderHarness(qc, queryFn, true);
    await screen.findByText('DATA: loaded');

    await qc.refetchQueries({ queryKey: ['thing'] });

    // Still showing the last-loaded data, not torn down into QueryError —
    // this is the regression itself.
    expect(await screen.findByText('DATA: loaded')).toBeInTheDocument();
    expect(screen.queryByText('Something went wrong')).not.toBeInTheDocument();

    const toasts = await screen.findAllByRole('status');
    expect(toasts).toHaveLength(1);
    expect(toasts[0]).toHaveTextContent('Could not refresh this page — showing the last loaded data.');
  });

  it('redirects to /login on a 401 background refetch failure and clears the whole query cache (I3)', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    // Data some other page loaded for the same session — the reset must
    // drop it too, not just the query that saw the 401.
    qc.setQueryData(['hosts', 'acme', {}], { pages: [{ hosts: [{ id: 'h1' }] }], pageParams: [''] });
    let call = 0;
    const queryFn = vi.fn(() => {
      call += 1;
      if (call === 1) return Promise.resolve({ value: 'loaded' });
      return Promise.reject(new ApiError(401, { detail: 'session expired' }));
    });

    renderHarness(qc, queryFn);
    await screen.findByText('DATA: loaded');

    await qc.refetchQueries({ queryKey: ['thing'] });

    expect(await screen.findByText('LOGIN PAGE')).toBeInTheDocument();
    await waitFor(() => expect(qc.getQueryCache().getAll()).toHaveLength(0));
    expect(screen.queryByText(/DATA:/)).not.toBeInTheDocument();
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
  });

  it('clears the whole query cache on a first-load 401 as well (QueryError\'s own redirect)', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    qc.setQueryData(['hosts', 'acme', {}], { pages: [{ hosts: [{ id: 'h1' }] }], pageParams: [''] });

    renderHarness(qc, () => Promise.reject(new ApiError(401, { detail: 'login required' })));

    expect(await screen.findByText('LOGIN PAGE')).toBeInTheDocument();
    await waitFor(() => expect(qc.getQueryCache().getAll()).toHaveLength(0));
  });

  it.each([
    [403, 'You do not have access'],
    [404, 'Not found'],
  ])(
    'treats a %i on a background refetch as final: renders the error page instead of the stale data, without a toast (I3)',
    async (status, heading) => {
      const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      let call = 0;
      const queryFn = vi.fn(() => {
        call += 1;
        if (call === 1) return Promise.resolve({ value: 'loaded' });
        return Promise.reject(new ApiError(status, { detail: 'no longer yours' }));
      });

      renderHarness(qc, queryFn);
      await screen.findByText('DATA: loaded');

      await qc.refetchQueries({ queryKey: ['thing'] });

      expect(await screen.findByText(heading)).toBeInTheDocument();
      expect(screen.queryByText(/DATA:/)).not.toBeInTheDocument();
      expect(screen.queryByRole('status')).not.toBeInTheDocument();
    },
  );

  it('accepts an infinite query: a failed "load more" page keeps the loaded pages and toasts once', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    function InfiniteHarness() {
      const query = useInfiniteQuery({
        queryKey: ['paged-thing'],
        initialPageParam: 0,
        queryFn: ({ pageParam }) =>
          pageParam === 0
            ? Promise.resolve({ value: 'page one', next: 1 })
            : Promise.reject(new ApiError(500, { detail: 'boom' })),
        getNextPageParam: (last) => last.next,
        retry: false,
      });
      const guard = useQueryGuard(query);
      if (guard) return guard;
      return (
        <div>
          <div>PAGES: {query.data?.pages.map((p) => p.value).join(', ')}</div>
          <button type="button" onClick={() => void query.fetchNextPage()}>
            next
          </button>
        </div>
      );
    }

    const rootRoute = createRootRoute();
    const testRoute = createRoute({ getParentRoute: () => rootRoute, path: '/test', component: InfiniteHarness });
    const router = createRouter({
      routeTree: rootRoute.addChildren([testRoute]),
      history: createMemoryHistory({ initialEntries: ['/test'] }),
    });
    render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </QueryClientProvider>,
    );

    await screen.findByText('PAGES: page one');
    await user.click(screen.getByRole('button', { name: 'next' }));

    const toasts = await screen.findAllByRole('status');
    expect(toasts).toHaveLength(1);
    expect(toasts[0]).toHaveTextContent('Could not load more — showing what is already loaded.');
    expect(screen.getByText('PAGES: page one')).toBeInTheDocument();
  });

  it('never raises a background-refetch toast for a query that is disabled (options.enabled: false)', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let call = 0;
    function DisabledHarness() {
      const query = useQuery({
        queryKey: ['disabled-thing'],
        queryFn: () => {
          call += 1;
          if (call === 1) return Promise.resolve({ value: 'loaded' });
          return Promise.reject(new ApiError(500, { detail: 'boom' }));
        },
        retry: false,
      });
      const guard = useQueryGuard(query, { enabled: false });
      if (guard) return guard;
      return <div>DATA: {query.data?.value ?? 'none'}</div>;
    }

    const rootRoute = createRootRoute();
    const testRoute = createRoute({ getParentRoute: () => rootRoute, path: '/test', component: DisabledHarness });
    const routeTree = rootRoute.addChildren([testRoute]);
    const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ['/test'] }) });

    render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </QueryClientProvider>,
    );
    await screen.findByText('DATA: loaded');

    await qc.refetchQueries({ queryKey: ['disabled-thing'] });

    expect(await screen.findByText('DATA: loaded')).toBeInTheDocument();
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
  });
});
