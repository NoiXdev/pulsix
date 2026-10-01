import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { ItemsPage } from './ItemsPage.tsx';

const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });

const METRICS = {
  metrics: [
    { name: 'disk.used_pct', check_key: 'disk', kind: 'numeric' },
    { name: 'service.state', check_key: 'service', kind: 'discrete' },
  ],
};

const decision = (over: Record<string, unknown> = {}) => ({
  id: 'd1',
  scope: 'org',
  check_key: 'disk',
  match_kind: 'glob',
  pattern: '/mnt/backup*',
  decision: 'ignore',
  note: 'Backup volume runs hot on purpose.',
  created_at: '2026-09-01T00:00:00Z',
  updated_at: '2026-09-01T00:00:00Z',
  ...over,
});

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const checksRoute = createRoute({ getParentRoute: () => orgRoute, path: '/checks' });
  const checksIndexRoute = createRoute({ getParentRoute: () => checksRoute, path: '/', component: () => <div>CHECKS VIEW</div> });
  const rulesRoute = createRoute({ getParentRoute: () => checksRoute, path: '/rules', component: () => <div>RULES VIEW</div> });
  const itemsRoute = createRoute({ getParentRoute: () => checksRoute, path: '/items', component: ItemsPage });
  const hostRoute = createRoute({ getParentRoute: () => orgRoute, path: '/hosts/$id', component: () => <div>HOST VIEW</div> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([
      orgRoute.addChildren([checksRoute.addChildren([checksIndexRoute, rulesRoute, itemsRoute]), hostRoute]),
    ]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/checks/items'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('ItemsPage', () => {
  it('lists item decisions grouped by check, the organization-wide pattern reading as the broad stroke it is', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/rules/metrics': METRICS,
      'GET /api/v1/orgs/acme/item-decisions': {
        decisions: [
          decision({ id: 'd-host', scope: 'host', host_id: 'h1', pattern: '/mnt/backup', match_kind: 'exact', note: '' }),
          decision({ id: 'd-org' }),
          decision({ id: 'd-service', check_key: 'service', pattern: 'sshd', match_kind: 'exact' }),
        ],
      },
    });
    renderPage();

    // The check-key heading is a <div>; the per-row check Tag repeating the
    // same text is a <span> — selecting by tag tells the section heading
    // apart from a row's own badge.
    const diskHeading = await screen.findByText('disk', { selector: 'div' });
    const serviceHeading = screen.getByText('service', { selector: 'div' });
    expect(diskHeading).toBeInTheDocument();
    expect(serviceHeading).toBeInTheDocument();
    expect(screen.getByText('/mnt/backup*')).toBeInTheDocument();
    expect(screen.getByText('/mnt/backup')).toBeInTheDocument();
    expect(screen.getByText('sshd')).toBeInTheDocument();

    // The organization-wide decision (d-org) reads as the broad stroke:
    // it sits before the host-scoped override on the same check.
    const rows = screen.getAllByText(/^Organization$|^h1$/);
    expect(rows[0]).toHaveTextContent('Organization');
  });

  it('shows a viewer the decisions but no write affordances', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('viewer'),
      'GET /api/v1/orgs/acme/rules/metrics': METRICS,
      'GET /api/v1/orgs/acme/item-decisions': { decisions: [decision()] },
    });
    renderPage();

    expect(await screen.findByText('/mnt/backup*')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'New decision' })).not.toBeInTheDocument();
  });

  it('creates an item decision with the body the API documents', async () => {
    const user = userEvent.setup();
    let posted: Record<string, unknown> | undefined;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/rules/metrics': METRICS,
      'GET /api/v1/orgs/acme/item-decisions': { decisions: [] },
      'POST /api/v1/orgs/acme/item-decisions/preview': () => ({ matches: [], total: 0, capped: false }),
      'POST /api/v1/orgs/acme/item-decisions': (req: MockRequest) => {
        posted = req.body as Record<string, unknown>;
        return { decision: decision({ id: 'd9' }) };
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'New decision' }));
    await user.selectOptions(screen.getByLabelText('Check'), 'disk');
    await user.selectOptions(screen.getByLabelText('Match kind'), 'glob');
    await user.type(screen.getByLabelText('Pattern'), '/mnt/backup*');
    await user.type(screen.getByLabelText('Note'), 'Backup volume runs hot on purpose.');
    await user.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() =>
      expect(posted).toEqual({
        scope: 'org',
        check_key: 'disk',
        match_kind: 'glob',
        pattern: '/mnt/backup*',
        decision: 'ignore',
        note: 'Backup volume runs hot on purpose.',
      }),
    );
  });

  it('deletes a decision once the operator has confirmed it', async () => {
    const user = userEvent.setup();
    const confirmed = vi.spyOn(window, 'confirm').mockReturnValue(true);
    let deleted = 0;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/rules/metrics': METRICS,
      'GET /api/v1/orgs/acme/item-decisions': { decisions: [decision()] },
      'DELETE /api/v1/orgs/acme/item-decisions/d1': () => {
        deleted += 1;
        return {};
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'Delete decision for /mnt/backup*' }));
    await waitFor(() => expect(deleted).toBe(1));
    confirmed.mockRestore();
  });

  it('keeps the section tabs when the decisions list fails', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/rules/metrics': METRICS,
      'GET /api/v1/orgs/acme/item-decisions': () =>
        new Response('{"detail":"boom"}', { status: 500, headers: { 'Content-Type': 'application/json' } }),
    });
    renderPage();

    expect(await screen.findByText('Something went wrong')).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Rules' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Items' })).toBeInTheDocument();
  });
});
