import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { ConnectionTestCard } from './ConnectionTestCard.tsx';


const test = (over: Record<string, unknown> = {}) => ({
  at: '2026-09-15T08:00:09Z',
  ok: true,
  host_key: 'SHA256:AAAA0AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA',
  host_key_state: 'unpinned',
  checks: [
    { name: 'ssh', detail: 'ok', ok: true },
    { name: 'auth', detail: 'publickey', ok: true },
    { name: 'sudo', detail: 'denied', ok: false },
  ],
  ...over,
});

function renderCard(org = 'acme', role = 'operator', hostId = 'h1') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/o/$org',
    component: () => (
      <>
        <ConnectionTestCard org={org} hostId={hostId} canWrite={role !== 'viewer'} />
        <Outlet />
      </>
    ),
  });
  const runRoute = createRoute({ getParentRoute: () => orgRoute, path: '/runs/$id', component: () => <div>RUN VIEW</div> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([runRoute])]),
    history: createMemoryHistory({ initialEntries: [`/o/${org}`] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('ConnectionTestCard', () => {
  it('renders nothing without a test and offers the run button to an operator', async () => {
    const user = userEvent.setup();
    let posted = false;
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/connection-test': () =>
        new Response(JSON.stringify({ status: 404, detail: 'no test yet' }), { status: 404, headers: { 'Content-Type': 'application/json' } }),
      'POST /api/v1/orgs/acme/hosts/h1/connection-test': () => {
        posted = true;
        return { job_id: 'job-1' };
      },
    });
    renderCard();
    expect(await screen.findByText('No connection test yet.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Run connection test' })).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Run connection test' }));
    expect(posted).toBe(true);
    expect(await screen.findByText('RUN VIEW')).toBeInTheDocument();
  });

  it('shows the latest result with per-check rows in the state colours', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/connection-test': { test: test({}) },
    });
    renderCard();
    const pills = await screen.findAllByText('ok');
    expect(pills.length).toBeGreaterThanOrEqual(2); // the result pill and the passing ssh check
    expect(screen.getByText('publickey')).toBeInTheDocument();
    const denied = screen.getByText('denied');
    expect(denied.style.color).toBe('var(--px-crit)');
  });

  it('reports a failed test and a changed host key in crit with the blocked hint', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/connection-test': {
        test: test({ ok: false, host_key_state: 'changed', checks: [{ name: 'ssh', detail: 'connection refused', ok: false }] }),
      },
    });
    renderCard();
    expect(await screen.findByText('failed')).toBeInTheDocument();
    const changed = screen.getAllByText('changed').find((el) => el.textContent === 'changed');
    expect(changed?.style.color).toBe('var(--px-crit)');
    expect(screen.getByText(/jobs are blocked until the new key is pinned/i)).toBeInTheDocument();
  });

  it('pins the fingerprint the latest test observed and unpins again', async () => {
    const user = userEvent.setup();
    let pinned: unknown;
    let unpinned = false;
    let state = 'unpinned';
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/connection-test': () => ({ test: test({ host_key_state: state }) }),
      'PUT /api/v1/orgs/acme/hosts/h1/host-key': (req: MockRequest) => {
        pinned = req.body;
        state = 'pinned';
        return {};
      },
      'DELETE /api/v1/orgs/acme/hosts/h1/host-key': () => {
        unpinned = true;
        state = 'unpinned';
        return {};
      },
    });
    renderCard();
    expect(await screen.findByText('unpinned')).toBeInTheDocument();
    expect(screen.getByText('SHA256:AAAA0AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Pin host key' }));
    expect(pinned).toEqual({ fingerprint: 'SHA256:AAAA0AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA' });
    await waitFor(() => expect(screen.getByRole('button', { name: 'Unpin' })).toBeInTheDocument());
    await user.click(screen.getByRole('button', { name: 'Unpin' }));
    expect(unpinned).toBe(true);
    await waitFor(() => expect(screen.getByRole('button', { name: 'Pin host key' })).toBeInTheDocument());
  });

  it('is read-only for a viewer: no run, pin or unpin buttons', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/connection-test': { test: test({ host_key_state: 'pinned' }) },
    });
    renderCard('acme', 'viewer');
    expect(await screen.findByText('pinned')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Run connection test' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Unpin' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Pin host key' })).not.toBeInTheDocument();
  });

  it('shows the pin needs confirmation badge and hint for a fingerprint-only pin (backlog 2.43)', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/connection-test': {
        test: test({ host_key_state: 'pinned', pin_state: 'fingerprint_only' }),
      },
    });
    renderCard();
    expect(await screen.findByText('pin needs confirmation')).toBeInTheDocument();
    expect(screen.getByText(/the pin holds only a fingerprint/i)).toBeInTheDocument();
  });

  it('shows the pin complete badge for a full pin and nothing while unpinned', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/connection-test': {
        test: test({ host_key_state: 'pinned', pin_state: 'full' }),
      },
    });
    renderCard();
    expect(await screen.findByText('pin complete')).toBeInTheDocument();
    expect(screen.queryByText('pin needs confirmation')).not.toBeInTheDocument();
  });
});
