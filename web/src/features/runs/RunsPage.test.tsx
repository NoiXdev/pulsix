import { describe, expect, it } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
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
      trigger: typeof s.trigger === 'string' ? s.trigger : undefined,
      schedule: typeof s.schedule === 'string' ? s.schedule : undefined,
      host: typeof s.host === 'string' ? s.host : undefined,
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

const ORG_OPERATOR = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } };
const ORG_VIEWER = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'viewer', created_at: '' } };

describe('RunsPage', () => {
  it('lists runs with kind, status colours, template, zone and trigger', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
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
    const seen: string[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': (req: { search: URLSearchParams }) => {
        seen.push(req.search.toString());
        return { jobs: [], next_cursor: '' };
      },
    });
    renderPage();
    await screen.findByLabelText('Filter by status');
    await pickOption(screen, 'Filter by status', { value: 'failed' });
    expect(await screen.findByText('No runs match.')).toBeInTheDocument();
    expect(seen.some((q) => q.includes('status=failed'))).toBe(true);
  });

  it('loads the next page through the cursor', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': (req: { search: URLSearchParams }) =>
        req.search.get('cursor')
          ? { jobs: [job({ id: '0197ffff-0000-7000-8000-00000000000b' })], next_cursor: '' }
          : { jobs: [job()], next_cursor: 'next-token' },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Load more' }));
    expect(await screen.findByText('0197ffff')).toBeInTheDocument();
  });

  it('offers Run command to an operator and opens the ad-hoc dialog', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': { jobs: [], next_cursor: '' },
      'GET /api/v1/orgs/acme/hosts': { hosts: [], next_cursor: '' },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Run command' }));
    expect(await screen.findByRole('dialog', { name: 'Run command' })).toBeInTheDocument();
  });

  it('hides Run command from a viewer', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': ORG_VIEWER,
      'GET /api/v1/orgs/acme/jobs': { jobs: [], next_cursor: '' },
    });
    renderPage();
    await screen.findByText('No runs match.');
    expect(screen.queryByRole('button', { name: 'Run command' })).not.toBeInTheDocument();
  });

  it('filters by trigger and, for scheduled runs, by schedule', async () => {
    const seen: string[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': (req: MockRequest) => {
        seen.push(req.search.toString());
        return { jobs: [job({ trigger: 'schedule' })], next_cursor: '' };
      },
      'GET /api/v1/orgs/acme/schedules': { schedules: [{ id: 's1', name: 'Nightly' }] },
    });
    renderPage();
    expect(await screen.findByText('Schedule', { selector: 'span' })).toBeInTheDocument();
    await pickOption(screen, 'Filter by trigger', { value: 'schedule' });
    await screen.findByLabelText('Filter by schedule');
    await pickOption(screen, 'Filter by schedule', { value: 's1' });
    expect(seen.some((q) => q.includes('trigger=schedule') && q.includes('trigger_ref=s1'))).toBe(true);
    await pickOption(screen, 'Filter by trigger', { value: 'webhook' });
    expect(screen.queryByLabelText('Filter by schedule')).not.toBeInTheDocument();
  });

  it('filters by host from the URL, names it in a chip and drops it when the chip is removed', async () => {
    const user = userEvent.setup();
    const seen: string[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': (req: MockRequest) => {
        seen.push(req.search.toString());
        return { jobs: [job()], next_cursor: '' };
      },
      'GET /api/v1/orgs/acme/hosts/h1': { host: { id: 'h1', name: 'web-04.fra', zone_id: 'z1', address: '10.0.0.4' } },
    });
    renderPage('/o/acme/runs?host=h1');
    expect(await screen.findByText('Host: web-04.fra')).toBeInTheDocument();
    expect(seen.some((q) => q.includes('host_id=h1'))).toBe(true);

    await user.click(screen.getByRole('button', { name: 'Remove host filter' }));
    await waitFor(() => expect(screen.queryByText('Host: web-04.fra')).not.toBeInTheDocument());
    expect(seen[seen.length - 1]).not.toContain('host_id=h1');
  });

  it('keeps the host filter when another filter changes', async () => {
    const seen: string[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': ORG_OPERATOR,
      'GET /api/v1/orgs/acme/jobs': (req: MockRequest) => {
        seen.push(req.search.toString());
        return { jobs: [], next_cursor: '' };
      },
      'GET /api/v1/orgs/acme/hosts/h1': { host: { id: 'h1', name: 'web-04.fra', zone_id: 'z1', address: '10.0.0.4' } },
    });
    renderPage('/o/acme/runs?host=h1');
    await screen.findByLabelText('Filter by status');
    await pickOption(screen, 'Filter by status', { value: 'failed' });
    await waitFor(() => expect(seen.some((q) => q.includes('status=failed') && q.includes('host_id=h1'))).toBe(true));
  });
});
