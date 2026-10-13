import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { AlertRoutesPage } from './AlertRoutesPage.tsx';

const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });
const me = { user: { id: 'u1', email: 'a@acme.io', display_name: 'A' }, mfa_complete: true, mfa_enrolment_required: false, totp_confirmed: true, instance_admin: false };
const channel = { id: 'c1', kind: 'email', name: 'Ops mail', enabled: true, revision: 1, created_at: '', updated_at: '', email: { recipients: ['ops@example.test'] } };

const route = (over: Record<string, unknown> = {}) => ({
  id: 'r1',
  name: 'Critical to ops',
  enabled: true,
  scope: 'org',
  severities: ['crit'],
  events: ['opened', 'severity_raised'],
  channels: ['c1'],
  revision: 1,
  created_at: '',
  updated_at: '',
  ...over,
});

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const page = createRoute({ getParentRoute: () => orgRoute, path: '/settings/alert-routes', component: AlertRoutesPage });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([page])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/settings/alert-routes'] }),
  });
  render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('AlertRoutesPage', () => {
  it('lists routes with their scope, severities, events and channels', async () => {
    mockApi({
      'GET /api/v1/auth/me': me,
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/alert-routes': { routes: [route()] },
      'GET /api/v1/orgs/acme/notification-channels': { channels: [channel], mail_available: true },
      'GET /api/v1/orgs/acme/hosts': { hosts: [], next_cursor: '' },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
    });
    renderPage();

    const row = (await screen.findByText('Critical to ops')).closest('[data-row]')!.parentElement as HTMLElement;
    expect(within(row).getByText('Organization')).toBeInTheDocument();
    expect(within(row).getByText('Critical')).toBeInTheDocument();
    expect(within(row).getByText('opened, worsened')).toBeInTheDocument();
    expect(within(row).getByText('Ops mail')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'New route' })).toBeInTheDocument();
    expect(screen.getByRole('switch', { name: 'Disable Critical to ops' })).toBeInTheDocument();
  });

  it('viewers are told alert routes are for operators', async () => {
    mockApi({ 'GET /api/v1/auth/me': me, 'GET /api/v1/orgs/acme': org('viewer') });
    renderPage();
    expect(await screen.findByText('Alert routes are visible to operators and above.')).toBeInTheDocument();
  });

  it('toggles a route from the list without opening the dialog', async () => {
    const user = userEvent.setup();
    let patched: { body: unknown; ifMatch: string | null } | undefined;
    mockApi({
      'GET /api/v1/auth/me': me,
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/alert-routes': { routes: [route()] },
      'GET /api/v1/orgs/acme/notification-channels': { channels: [channel], mail_available: true },
      'GET /api/v1/orgs/acme/hosts': { hosts: [], next_cursor: '' },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'PATCH /api/v1/orgs/acme/alert-routes/r1': (req: MockRequest) => {
        patched = { body: req.body, ifMatch: req.headers.get('If-Match') };
        return { route: route({ enabled: false }) };
      },
    });
    renderPage();

    await user.click(await screen.findByRole('switch', { name: 'Disable Critical to ops' }));
    await waitFor(() => expect(patched?.ifMatch).toBe('"1"'));
    expect(patched?.body).toEqual({
      name: 'Critical to ops', enabled: false, scope: 'org', severities: ['crit'], events: ['opened', 'severity_raised'], channels: ['c1'],
    });
  });

  it('creates a route with the body the API documents', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi({
      'GET /api/v1/auth/me': me,
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/alert-routes': { routes: [] },
      'GET /api/v1/orgs/acme/notification-channels': { channels: [channel], mail_available: true },
      'GET /api/v1/orgs/acme/hosts': { hosts: [], next_cursor: '' },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'POST /api/v1/orgs/acme/alert-routes': (req: MockRequest) => {
        posted = req.body;
        return { route: route({ id: 'r9' }) };
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'New route' }));
    const dialog = screen.getByRole('dialog', { name: 'New route' });
    await user.type(within(dialog).getByLabelText(/name/i), 'Critical to ops');
    await user.click(within(dialog).getByRole('checkbox', { name: /critical/i }));
    await user.click(within(dialog).getByRole('checkbox', { name: /opened/i }));
    await user.click(within(dialog).getByRole('checkbox', { name: /ops mail/i }));
    await user.click(within(dialog).getByRole('button', { name: /save/i }));

    await waitFor(() =>
      expect(posted).toEqual({ name: 'Critical to ops', enabled: true, scope: 'org', severities: ['crit'], events: ['opened'], channels: ['c1'] }),
    );
  });

  it('edits a route and sends If-Match with its revision', async () => {
    const user = userEvent.setup();
    let patched: { body: unknown; ifMatch: string | null } | undefined;
    mockApi({
      'GET /api/v1/auth/me': me,
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/alert-routes': { routes: [route({ revision: 3 })] },
      'GET /api/v1/orgs/acme/notification-channels': { channels: [channel], mail_available: true },
      'GET /api/v1/orgs/acme/hosts': { hosts: [], next_cursor: '' },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'PATCH /api/v1/orgs/acme/alert-routes/r1': (req: MockRequest) => {
        patched = { body: req.body, ifMatch: req.headers.get('If-Match') };
        return { route: route({ revision: 4 }) };
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'Edit Critical to ops' }));
    const dialog = screen.getByRole('dialog', { name: 'Edit Critical to ops' });
    await user.click(within(dialog).getByRole('button', { name: /save/i }));

    await waitFor(() => expect(patched?.ifMatch).toBe('"3"'));
    expect(patched?.body).toEqual({
      name: 'Critical to ops', enabled: true, scope: 'org', severities: ['crit'], events: ['opened', 'severity_raised'], channels: ['c1'],
    });
  });

  it('says a route whose last channel was deleted reaches nobody', async () => {
    // Decision 8: a route that stops working says so. Deleting a channel
    // removes the link, not the route, so the route survives with an
    // empty channel set — which rendered as an empty Channels cell, the
    // one thing indistinguishable from "not loaded".
    mockApi({
      'GET /api/v1/auth/me': me,
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/alert-routes': { routes: [route({ channels: [] })] },
      'GET /api/v1/orgs/acme/notification-channels': { channels: [], mail_available: true },
      'GET /api/v1/orgs/acme/hosts': { hosts: [], next_cursor: '' },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
    });
    renderPage();

    expect(await screen.findByText(/reaches nobody/i)).toBeInTheDocument();
  });

  it('does not call a route with channels ineffective', async () => {
    mockApi({
      'GET /api/v1/auth/me': me,
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/alert-routes': { routes: [route()] },
      'GET /api/v1/orgs/acme/notification-channels': { channels: [channel], mail_available: true },
      'GET /api/v1/orgs/acme/hosts': { hosts: [], next_cursor: '' },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
    });
    renderPage();

    expect(await screen.findByText('Ops mail')).toBeInTheDocument();
    expect(screen.queryByText(/reaches nobody/i)).not.toBeInTheDocument();
  });

  it('deletes a route once confirmed', async () => {
    const user = userEvent.setup();
    const confirmed = vi.spyOn(window, 'confirm').mockReturnValue(true);
    let deleted = 0;
    mockApi({
      'GET /api/v1/auth/me': me,
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/alert-routes': { routes: [route()] },
      'GET /api/v1/orgs/acme/notification-channels': { channels: [channel], mail_available: true },
      'GET /api/v1/orgs/acme/hosts': { hosts: [], next_cursor: '' },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'DELETE /api/v1/orgs/acme/alert-routes/r1': () => {
        deleted += 1;
        return {};
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'Delete Critical to ops' }));
    await waitFor(() => expect(deleted).toBe(1));
    expect(confirmed.mock.calls[0][0]).toContain('Critical to ops');
    confirmed.mockRestore();
  });
});
