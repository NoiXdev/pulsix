import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { ChecksPage } from './ChecksPage.tsx';

const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });

const check = (over: Record<string, unknown> = {}) => ({
  id: 'c1', key: 'cpu', plugin: 'cpu', params: {}, interval_seconds: 60, enabled: true,
  origin: 'seed', binding_kind: 'org', created_at: '', updated_at: '', ...over,
});

const GROUPS = { groups: [{ id: 'g1', name: 'Web tier', selector: 'role=web', priority: 10, created_at: '', updated_at: '' }] };

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const checksRoute = createRoute({ getParentRoute: () => orgRoute, path: '/checks' });
  const indexRoute = createRoute({ getParentRoute: () => checksRoute, path: '/', component: ChecksPage });
  const groupsRoute = createRoute({ getParentRoute: () => checksRoute, path: '/monitoring-groups', component: () => <div>GROUPS VIEW</div> });
  const hostRoute = createRoute({ getParentRoute: () => orgRoute, path: '/hosts/$id', component: () => <div>HOST VIEW</div> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([checksRoute.addChildren([indexRoute, groupsRoute]), hostRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/checks'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('ChecksPage', () => {
  it('lists definitions with their binding, interval, state and origin', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/hosts/h1': { host: { id: 'h1', name: 'web-1' } },
      'GET /api/v1/orgs/acme/checks': {
        checks: [
          check(),
          check({ id: 'c2', key: 'disk-data', plugin: 'disk', interval_seconds: 300, origin: 'custom', binding_kind: 'group', group_id: 'g1' }),
          check({ id: 'c3', key: 'nginx', plugin: 'service', interval_seconds: 30, enabled: false, origin: 'custom', binding_kind: 'host', host_id: 'h1' }),
        ],
      },
    });
    renderPage();

    // The seeded cpu row shows its key and its plugin, both "cpu".
    expect(await screen.findAllByText('cpu')).toHaveLength(2);
    // Every binding kind reads as what it binds to, never as a raw id.
    expect(screen.getByText('Organization')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Web tier' })).toBeInTheDocument();
    expect(await screen.findByRole('link', { name: 'web-1' })).toBeInTheDocument();
    // Origin is on the row, not hidden inside a dialog.
    expect(screen.getByText('seed')).toBeInTheDocument();
    expect(screen.getAllByText('custom')).toHaveLength(2);
    expect(screen.getByText('1 min')).toBeInTheDocument();
    expect(screen.getByText('5 min')).toBeInTheDocument();
    expect(screen.getByText('30 s')).toBeInTheDocument();
    expect(screen.getByText('disabled')).toBeInTheDocument();
    // A seeded row offers the copy that customising its key or plugin needs.
    expect(screen.getByRole('button', { name: 'Copy cpu' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Copy disk-data' })).not.toBeInTheDocument();
  });

  it('shows a viewer the definitions but no write affordances', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('viewer'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [check(), check({ id: 'c2', key: 'disk-data', plugin: 'disk', origin: 'custom' })] },
    });
    renderPage();

    expect(await screen.findAllByText('cpu')).toHaveLength(2);
    expect(screen.queryByRole('button', { name: 'New check' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Edit cpu' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Copy cpu' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Delete disk-data' })).not.toBeInTheDocument();
  });

  it('creates a definition with the body the API documents', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [] },
      'POST /api/v1/orgs/acme/checks': (req: MockRequest) => {
        posted = req.body;
        return { check: check({ id: 'c9', key: 'nginx', plugin: 'service', origin: 'custom' }) };
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'New check' }));
    await user.selectOptions(screen.getByLabelText('Plugin'), 'service');
    await user.type(screen.getByLabelText('Key'), 'nginx');
    await user.clear(screen.getByLabelText('Interval (seconds)'));
    await user.type(screen.getByLabelText('Interval (seconds)'), '30');
    await user.type(screen.getByLabelText('Units (one per line)'), 'nginx\nphp-fpm');
    await user.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() =>
      expect(posted).toEqual({
        plugin: 'service',
        key: 'nginx',
        params: { units: ['nginx', 'php-fpm'] },
        interval_seconds: 30,
        enabled: true,
        binding_kind: 'org',
      }),
    );
  });

  it('keeps a seeded key and plugin read-only while still retuning its interval', async () => {
    const user = userEvent.setup();
    let put: unknown;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [check()] },
      'PUT /api/v1/orgs/acme/checks/c1': (req: MockRequest) => {
        put = req.body;
        return { check: check({ interval_seconds: 120 }) };
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'Edit cpu' }));
    expect(screen.getByLabelText('Key')).toBeDisabled();
    expect(screen.getByLabelText('Plugin')).toBeDisabled();
    expect(screen.getByLabelText('Interval (seconds)')).not.toBeDisabled();

    await user.clear(screen.getByLabelText('Interval (seconds)'));
    await user.type(screen.getByLabelText('Interval (seconds)'), '120');
    await user.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() =>
      expect(put).toEqual({ plugin: 'cpu', key: 'cpu', params: {}, interval_seconds: 120, enabled: true, binding_kind: 'org' }),
    );
  });

  it('binds a definition to a single monitored host', async () => {
    const user = userEvent.setup();
    let posted: Record<string, unknown> | undefined;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [] },
      'GET /api/v1/orgs/acme/hosts': { hosts: [{ id: 'h1', name: 'web-1' }], next_cursor: '' },
      'POST /api/v1/orgs/acme/checks': (req: MockRequest) => {
        posted = req.body as Record<string, unknown>;
        return { check: check({ id: 'c9', origin: 'custom' }) };
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'New check' }));
    await user.selectOptions(screen.getByLabelText('Binds to'), 'host');
    await user.selectOptions(await screen.findByLabelText('Host'), 'h1');
    await user.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(posted).toMatchObject({ binding_kind: 'host', host_id: 'h1', plugin: 'cpu' }));
    expect(posted).not.toHaveProperty('group_id');
  });

  it('deletes a definition once the operator has confirmed it', async () => {
    const user = userEvent.setup();
    const confirmed = vi.spyOn(window, 'confirm').mockReturnValue(true);
    let deleted = 0;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [check({ id: 'c2', key: 'disk-data', plugin: 'disk', origin: 'custom' })] },
      'DELETE /api/v1/orgs/acme/checks/c2': () => {
        deleted += 1;
        return {};
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'Delete disk-data' }));
    await waitFor(() => expect(deleted).toBe(1));
    expect(confirmed.mock.calls[0][0]).toContain('disk-data');
    confirmed.mockRestore();
  });

  it('copies a seeded definition into a custom one whose key can change', async () => {
    const user = userEvent.setup();
    let posted: Record<string, unknown> | undefined;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [check({ plugin: 'service', key: 'service', params: { units: ['sshd'] } })] },
      'POST /api/v1/orgs/acme/checks': (req: MockRequest) => {
        posted = req.body as Record<string, unknown>;
        return { check: check({ id: 'c9', origin: 'custom' }) };
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'Copy service' }));
    // The copy is a create: the key is editable, and the plugin and its
    // parameters come across unchanged.
    expect(screen.getByLabelText('Key')).not.toBeDisabled();
    expect(screen.getByLabelText('Units (one per line)')).toHaveValue('sshd');
    await user.clear(screen.getByLabelText('Key'));
    await user.type(screen.getByLabelText('Key'), 'service-app');
    await user.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() =>
      expect(posted).toMatchObject({ key: 'service-app', plugin: 'service', params: { units: ['sshd'] }, binding_kind: 'org' }),
    );
  });
});
