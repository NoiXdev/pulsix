import { describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { AgentsPage } from './AgentsPage.tsx';

const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });
const inDays = (d: number) => new Date(Date.now() + d * 86_400_000).toISOString();
const agent = (over: Record<string, unknown> = {}) => ({
  host_id: 'h1', host_name: 'web-1', agent_name: 'web-1', status: 'online', last_seen: new Date().toISOString(),
  version: 'v0.3a', cert_expires_at: inDays(60), created_at: '', ...over,
});

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const listRoute = createRoute({ getParentRoute: () => orgRoute, path: '/agents', component: AgentsPage });
  const hostRoute = createRoute({ getParentRoute: () => orgRoute, path: '/hosts/$id', component: () => <div>HOST VIEW</div> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([listRoute, hostRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/agents'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('AgentsPage', () => {
  it('lists agents with their host, status, version and certificate expiry', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/agents': {
        agents: [agent(), agent({ host_id: 'h2', host_name: 'db-1', agent_name: 'db-1', status: 'offline', last_seen: null, cert_expires_at: inDays(5) })],
      },
      'GET /api/v1/orgs/acme/agents/tokens': {
        tokens: [{ id: 'aaaaaaaa-0000-7000-8000-000000000001', zone_id: 'z1', name_hint: 'edge-1', max_uses: 3, uses: 1, expires_at: inDays(0.02), created_at: '' }],
      },
    });
    renderPage();

    expect(await screen.findByRole('link', { name: 'web-1' })).toBeInTheDocument();
    expect(screen.getByText('online')).toBeInTheDocument();
    expect(screen.getByText('offline')).toBeInTheDocument();
    expect(screen.getByText('never')).toBeInTheDocument();
    expect(screen.getByText('1 of 2 reporting · agents collect a host’s monitoring data'.replace('’', "'"))).toBeInTheDocument();
    // The certificate closest to expiry is the highlighted one.
    expect(document.querySelector('[data-expiring]')?.textContent).toMatch(/in [45] d/);
    // The pending token shows how much of it is left, never its secret.
    expect(screen.getByText('edge-1')).toBeInTheDocument();
    expect(screen.getByText('1 / 3')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Revoke agent on web-1' })).toBeInTheDocument();
  });

  it('shows a viewer the fleet but no write actions', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('viewer'),
      'GET /api/v1/orgs/acme/agents': { agents: [agent()] },
      'GET /api/v1/orgs/acme/agents/tokens': { tokens: [] },
    });
    renderPage();

    expect(await screen.findByRole('link', { name: 'web-1' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Enrol agent' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Revoke agent on web-1' })).not.toBeInTheDocument();
  });

  it('creates a token and shows it once with the install command', async () => {
    const user = userEvent.setup();
    let posted: Record<string, unknown> | undefined;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/agents': { agents: [] },
      'GET /api/v1/orgs/acme/agents/tokens': { tokens: [] },
      'GET /api/v1/orgs/acme/zones': { zones: [{ id: 'z1', org_id: 'o', slug: 'hel', name: 'Helsinki' }] },
      'POST /api/v1/orgs/acme/agents/tokens': (req: MockRequest) => {
        posted = req.body as Record<string, unknown>;
        return {
          token: 'pxa1.aaa.bbb.ccc',
          token_record: { id: 't1', zone_id: 'z1', name_hint: 'edge-1', max_uses: 2, uses: 0, expires_at: inDays(1), created_at: '' },
          install_command: 'curl -fsSL https://pulsix.example/install.sh | sudo sh -s -- --manager pulsix.example:8444',
        };
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: /Enrol agent/ }));
    await user.type(screen.getByLabelText('Host name (optional)'), 'edge-1');
    await user.clear(screen.getByLabelText('Agents this token may enrol'));
    await user.type(screen.getByLabelText('Agents this token may enrol'), '2');
    await user.click(screen.getByRole('button', { name: 'Create token' }));

    expect(await screen.findByLabelText('Install command')).toHaveTextContent('--manager pulsix.example:8444');
    expect(screen.getByLabelText('Enrollment token')).toHaveTextContent('pxa1.aaa.bbb.ccc');
    expect(posted).toMatchObject({ zone_id: 'z1', name_hint: 'edge-1', max_uses: 2, expires_in_minutes: 60 });
  });

  // v0.4g Task 9: the Zone picker's create row. agents.write (operator) is
  // no stricter than hosts.write (also operator, zone creation's own
  // permission), so condition 1 holds — unlike credentials, which is
  // admin-only while the forms that reach it are operator-level.
  it('creates a zone from the picker and uses it for the enrollment', async () => {
    const user = userEvent.setup();
    let posted: Record<string, unknown> | undefined;
    let zonePosted: unknown;
    let created = false;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/agents': { agents: [] },
      'GET /api/v1/orgs/acme/agents/tokens': { tokens: [] },
      // A function, not a fixed list: the picker's options must include
      // the new zone after creation invalidates this query, not merely
      // whatever onCreated's id points at (v0.4g Task 5's "b9
      // (unavailable)" lesson).
      'GET /api/v1/orgs/acme/zones': () => ({ zones: created ? [{ id: 'z9', org_id: 'o', slug: 'frankfurt', name: 'Frankfurt' }] : [] }),
      'POST /api/v1/orgs/acme/zones': (req: MockRequest) => {
        zonePosted = req.body;
        created = true;
        return { zone: { id: 'z9', slug: 'frankfurt', name: 'Frankfurt' } };
      },
      'POST /api/v1/orgs/acme/agents/tokens': (req: MockRequest) => {
        posted = req.body as Record<string, unknown>;
        return { token: 'x', token_record: {}, install_command: '' };
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: /Enrol agent/ }));
    await user.click(screen.getByLabelText('Zone'));
    await user.keyboard('frankfurt');
    await user.click(screen.getByRole('option', { name: /New zone/ }));

    const zoneDialog = await screen.findByRole('dialog', { name: 'Add zone' });
    expect(within(zoneDialog).getByLabelText('Name')).toHaveValue('frankfurt');
    await user.type(within(zoneDialog).getByLabelText('Slug'), 'frankfurt');
    await user.click(within(zoneDialog).getByRole('button', { name: 'Create zone' }));

    expect(zonePosted).toMatchObject({ name: 'frankfurt' });
    // Not findByLabelText: the field is already in the DOM before the
    // zones list refetch (triggered by the mutation's own invalidation)
    // completes, so the assertion itself has to retry, not just the lookup.
    await waitFor(() => expect(screen.getByLabelText('Zone')).toHaveValue('Frankfurt (frankfurt)'));

    await user.click(screen.getByRole('button', { name: 'Create token' }));
    expect(posted).toMatchObject({ zone_id: 'z9' });
  });

  it('refuses a host name the server would refuse, before asking it', async () => {
    const user = userEvent.setup();
    let calls = 0;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/agents': { agents: [] },
      'GET /api/v1/orgs/acme/agents/tokens': { tokens: [] },
      'GET /api/v1/orgs/acme/zones': { zones: [{ id: 'z1', org_id: 'o', slug: 'hel', name: 'Helsinki' }] },
      'POST /api/v1/orgs/acme/agents/tokens': () => {
        calls += 1;
        return { token: 'x', token_record: {}, install_command: '' };
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: /Enrol agent/ }));
    await user.type(screen.getByLabelText('Host name (optional)'), 'Not A Host Name');
    await user.click(screen.getByRole('button', { name: 'Create token' }));

    expect(await screen.findByText(/lowercase letters, digits and hyphens/)).toBeInTheDocument();
    expect(calls).toBe(0);
  });
});
