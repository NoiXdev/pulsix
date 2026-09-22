import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { JumpHostsPage } from './JumpHostsPage.tsx';

const ME = { id: 'u', email: 'a@b', display_name: 'A', instance_admin: false };
const BASTIONS = {
  bastions: [
    { id: 'b1', name: 'bastion-a', address: 'bastion-a.example', port: 22, username: 'ops', credential_id: 'c1' },
    { id: 'b2', name: 'bastion-b', address: 'bastion-b.example', port: 2222, username: 'ops', via_bastion_id: 'b1' },
  ],
};
const CREDS = { credentials: [{ id: 'c1', name: 'inv-deploy', kind: 'ssh_key', username: 'ansible', expired: false, expires_soon: false, used_by: { bastions: 1, hosts: 0 }, created_at: '', updated_at: '' }] };
const ZONES = { zones: [{ id: 'z1', slug: 'primary', name: 'Primary' }] };

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const settings = createRoute({ getParentRoute: () => orgRoute, path: '/settings' });
  const page = createRoute({ getParentRoute: () => settings, path: '/jump-hosts', component: JumpHostsPage });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([settings.addChildren([page])])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/settings/jump-hosts'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

const base = {
  'GET /api/v1/auth/me': ME,
  'GET /api/v1/orgs/acme/bastions': BASTIONS,
  'GET /api/v1/orgs/acme/credentials': CREDS,
  'GET /api/v1/orgs/acme/zones': ZONES,
};

describe('JumpHostsPage', () => {
  it('lists jump hosts with their login, chain and credential', async () => {
    mockApi(base);
    renderPage();
    expect(await screen.findByText('ops@bastion-a.example:22')).toBeInTheDocument();
    expect(screen.getByText('via bastion-a')).toBeInTheDocument();
    expect(screen.getByText('inv-deploy')).toBeInTheDocument();
  });

  it('adds a chained jump host', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi({
      ...base,
      'POST /api/v1/orgs/acme/bastions': (req: MockRequest) => {
        posted = req.body;
        return { bastion: { id: 'b3', name: 'edge', address: 'edge.example', port: 2222, username: 'ops' } };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add jump host' }));
    const dialog = screen.getByRole('dialog', { name: 'Add jump host' });
    await user.type(within(dialog).getByLabelText('Name'), 'edge');
    await user.type(within(dialog).getByLabelText('Address'), 'edge.example');
    await user.clear(within(dialog).getByLabelText('Port'));
    await user.type(within(dialog).getByLabelText('Port'), '2222');
    await user.type(within(dialog).getByLabelText('Username'), 'ops');
    await user.selectOptions(within(dialog).getByLabelText('Credential'), 'c1');
    await user.selectOptions(within(dialog).getByLabelText('Reached through'), 'b1');
    await user.click(within(dialog).getByRole('button', { name: 'Save jump host' }));
    expect(await screen.findByText('Jump host edge saved')).toBeInTheDocument();
    expect(posted).toEqual({ name: 'edge', address: 'edge.example', port: 2222, username: 'ops', credential_id: 'c1', via_bastion_id: 'b1' });
  });

  it('sets and clears a zone default', async () => {
    const user = userEvent.setup();
    const bodies: unknown[] = [];
    let zoneBastion: string | undefined;
    mockApi({
      ...base,
      // The select shows what the server stores, so the mock stores it too.
      'GET /api/v1/orgs/acme/zones': () => ({ zones: [{ ...ZONES.zones[0], bastion_id: zoneBastion }] }),
      'PUT /api/v1/orgs/acme/zones/z1/bastion': (req: MockRequest) => {
        bodies.push(req.body);
        zoneBastion = (req.body as { bastion_id?: string }).bastion_id;
        return {};
      },
    });
    renderPage();
    const select = await screen.findByLabelText(/Primary/);
    await user.selectOptions(select, 'b1');
    await waitFor(() => expect(select).toHaveValue('b1'));
    await user.selectOptions(select, '');
    await waitFor(() => expect(select).toHaveValue(''));
    expect(bodies).toEqual([{ bastion_id: 'b1' }, {}]);
  });

  it('shows why a jump host cannot be deleted', async () => {
    const user = userEvent.setup();
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    mockApi({
      ...base,
      'DELETE /api/v1/orgs/acme/bastions/b1': () =>
        new Response(JSON.stringify({ status: 409, detail: 'the jump host is still used by a zone, group, host or another jump host' }), {
          status: 409, headers: { 'Content-Type': 'application/json' },
        }),
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Delete bastion-a' }));
    expect(await screen.findByText(/still used by a zone/)).toBeInTheDocument();
  });

  it('reports a failed zone default save as a toast', async () => {
    const user = userEvent.setup();
    mockApi({
      ...base,
      'PUT /api/v1/orgs/acme/zones/z1/bastion': () =>
        new Response(JSON.stringify({ status: 500, detail: 'the zone could not be updated' }), {
          status: 500, headers: { 'Content-Type': 'application/json' },
        }),
    });
    renderPage();
    const select = await screen.findByLabelText(/Primary/);
    await user.selectOptions(select, 'b1');
    expect(await screen.findByText('the zone could not be updated')).toBeInTheDocument();
  });

  // --- Host-key pins (backlog 2.40) -------------------------------------
  const FP = 'SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA';

  it('shows the pinned fingerprint and the pin state badge, and unpins', async () => {
    const user = userEvent.setup();
    mockApi({
      ...base,
      'GET /api/v1/orgs/acme': { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'admin', created_at: '' } },
      'GET /api/v1/orgs/acme/bastions': {
        bastions: [{ ...BASTIONS.bastions[0], ssh_host_key: FP, pin_state: 'full', observed_key: FP, observed_state: 'pinned' }],
      },
      'DELETE /api/v1/orgs/acme/bastions/b1/host-key': () => ({}),
    });
    renderPage();
    expect(await screen.findByText(FP)).toBeInTheDocument();
    expect(screen.getByText('pin complete')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Unpin bastion-a host key' }));
    expect(await screen.findByText('Jump host pin cleared')).toBeInTheDocument();
  });

  it('pins the observed fingerprint of an unpinned jump host', async () => {
    const user = userEvent.setup();
    let pinned: unknown;
    mockApi({
      ...base,
      'GET /api/v1/orgs/acme': { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'admin', created_at: '' } },
      'GET /api/v1/orgs/acme/bastions': {
        bastions: [{ ...BASTIONS.bastions[0], observed_key: FP, observed_state: 'unpinned' }],
      },
      'PUT /api/v1/orgs/acme/bastions/b1/host-key': (req: MockRequest) => {
        pinned = req.body;
        return {};
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Pin bastion-a host key' }));
    await waitFor(() => expect(pinned).toEqual({ fingerprint: FP }));
    expect(await screen.findByText('Jump host key of bastion-a pinned')).toBeInTheDocument();
  });

  it('flags a changed jump host key as blocking', async () => {
    mockApi({
      ...base,
      'GET /api/v1/orgs/acme': { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'admin', created_at: '' } },
      'GET /api/v1/orgs/acme/bastions': {
        bastions: [{ ...BASTIONS.bastions[0], ssh_host_key: FP, pin_state: 'full', observed_key: FP, observed_state: 'changed' }],
      },
    });
    renderPage();
    expect(await screen.findByText('key changed — connections through it are blocked')).toBeInTheDocument();
    // No unpin here — the operator first pins what the test saw.
    expect(screen.queryByRole('button', { name: 'Unpin bastion-a host key' })).not.toBeInTheDocument();
  });

  it('shows nothing interactive for a viewer (no pin/unpin)', async () => {
    mockApi({
      ...base,
      'GET /api/v1/orgs/acme': { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'viewer', created_at: '' } },
      'GET /api/v1/orgs/acme/bastions': {
        bastions: [{ ...BASTIONS.bastions[0], observed_key: FP, observed_state: 'unpinned' }],
      },
    });
    renderPage();
    expect(await screen.findByText(FP)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Pin bastion-a host key' })).not.toBeInTheDocument();
  });
});
