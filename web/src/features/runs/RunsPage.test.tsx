import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { RunsPage } from './RunsPage.tsx';

const job = (over: Record<string, unknown> = {}) => ({
  id: '0197c0a2-0000-7000-8000-000000000001', zone_id: '0197c0b1-0000-7000-8000-000000000002',
  kind: 'run', status: 'succeeded', run_group_id: 'g1', trigger: 'manual', created_at: '2026-09-15T08:00:00Z', ...over,
});

function renderPage(initial = '/o/acme/runs') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const listRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/runs',
    validateSearch: (s: Record<string, unknown>) => ({
      status: typeof s.status === 'string' ? s.status : undefined,
      kind: typeof s.kind === 'string' ? s.kind : undefined,
    }),
    component: RunsPage,
  });
  const detailRoute = createRoute({ getParentRoute: () => orgRoute, path: '/runs/$id', component: () => <div>RUN DETAIL</div> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([listRoute, detailRoute])]),
    history: createMemoryHistory({ initialEntries: [initial] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('RunsPage', () => {
  it('lists runs with kind, status colours, template, zone and trigger', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/jobs': {
        jobs: [
          job({ template_id: '0197c0c1-0000-7000-8000-000000000003' }),
          job({ id: '0197c0a2-0000-7000-8000-00000000000a', kind: 'connection_test', status: 'failed', template_id: undefined }),
        ],
        next_cursor: '',
      },
    });
    renderPage();
    expect(await screen.findByText('0197c0c1')).toBeInTheDocument();
    const kindCell = screen.getAllByText('connection test').find((el) => el.tagName !== 'OPTION');
    expect(kindCell).toBeDefined();
    const failed = screen.getAllByText('failed').find((el) => el.tagName !== 'OPTION');
    expect(failed).toHaveStyle({ color: 'var(--px-crit)' });
    expect(screen.getByText('0197c0c1')).toBeInTheDocument();
    const runCell = screen.getAllByText('run').find((el) => el.tagName !== 'OPTION');
    expect(runCell).toBeDefined();
  });

  it('filters by status through the URL', async () => {
    const user = userEvent.setup();
    const seen: string[] = [];
    mockApi({
      'GET /api/v1/orgs/acme/jobs': (req: { search: URLSearchParams }) => {
        seen.push(req.search.toString());
        return { jobs: [], next_cursor: '' };
      },
    });
    renderPage();
    await user.selectOptions(await screen.findByLabelText('Filter by status'), 'failed');
    expect(await screen.findByText('No runs match.')).toBeInTheDocument();
    expect(seen.some((q) => q.includes('status=failed'))).toBe(true);
  });

  it('loads the next page through the cursor', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme/jobs': (req: { search: URLSearchParams }) =>
        req.search.get('cursor')
          ? { jobs: [job({ id: '0197ffff-0000-7000-8000-00000000000b' })], next_cursor: '' }
          : { jobs: [job()], next_cursor: 'next-token' },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Load more' }));
    expect(await screen.findByText('0197ffff')).toBeInTheDocument();
  });
});
