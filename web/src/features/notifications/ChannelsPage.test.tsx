import { describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { ChannelsPage } from './ChannelsPage.tsx';

const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });
const me = { user: { id: 'u1', email: 'a@acme.io', display_name: 'A' }, mfa_complete: true, mfa_enrolment_required: false, totp_confirmed: true, instance_admin: false };
const hook = {
  id: 'c1', kind: 'webhook', name: 'ops hook', enabled: true, revision: 2, created_at: '', updated_at: '',
  webhook: { method: 'POST', url: 'https://hooks.example.test/…', headers: ['Authorization'], signed: true },
};
const mailCh = { id: 'c2', kind: 'email', name: 'ops mail', enabled: false, revision: 1, created_at: '', updated_at: '', email: { recipients: ['ops@example.test'] } };

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const page = createRoute({ getParentRoute: () => orgRoute, path: '/settings/notifications', component: ChannelsPage });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([page])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/settings/notifications'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('ChannelsPage', () => {
  it('lists channels with their target and status, and admins manage them', async () => {
    mockApi({
      'GET /api/v1/auth/me': me,
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/notification-channels': { channels: [hook, mailCh], mail_available: true },
    });
    renderPage();
    const row = (await screen.findByText('ops hook')).closest('[data-row]')!.parentElement as HTMLElement;
    expect(within(row).getByText('POST https://hooks.example.test/…')).toBeInTheDocument();
    expect(within(row).getByText('enabled')).toBeInTheDocument();
    expect(screen.getByText('ops@example.test')).toBeInTheDocument();
    expect(screen.getByText('disabled')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Add channel' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Send test through ops hook' })).toBeInTheDocument();
  });

  it('operators read channels and their log but cannot change them', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me': me,
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/notification-channels': { channels: [hook], mail_available: true },
      'GET /api/v1/orgs/acme/notification-channels/c1/deliveries': {
        deliveries: [
          { id: 'd1', event_kind: 'run.finished', status: 'pending', attempts: 2, last_error: 'the endpoint answered 503', next_attempt_at: '2026-09-22T08:02:00Z', created_at: '2026-09-22T08:00:00Z' },
          { id: 'd2', event_kind: 'test', status: 'sent', attempts: 1, created_at: '2026-09-22T07:00:00Z' },
        ],
      },
    });
    renderPage();
    await screen.findByText('ops hook');
    expect(screen.queryByRole('button', { name: 'Add channel' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Send test through ops hook' })).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Deliveries of ops hook' }));
    const dialog = await screen.findByRole('dialog', { name: 'Deliveries · ops hook' });
    expect(await within(dialog).findByText(/2 attempts · the endpoint answered 503/)).toBeInTheDocument();
    expect(within(dialog).getAllByText(/sent|pending/)).toHaveLength(2);
  });

  it('viewers are told channels are for operators', async () => {
    mockApi({ 'GET /api/v1/auth/me': me, 'GET /api/v1/orgs/acme': org('viewer') });
    renderPage();
    expect(await screen.findByText('Notification channels are visible to operators and above.')).toBeInTheDocument();
  });

  it('creates a signed webhook channel with a header', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi({
      'GET /api/v1/auth/me': me,
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/notification-channels': { channels: [], mail_available: false },
      'POST /api/v1/orgs/acme/notification-channels': (req: MockRequest) => {
        posted = req.body;
        return { channel: hook };
      },
    });
    renderPage();
    expect(await screen.findByRole('note')).toHaveTextContent('outgoing mail settings');
    await user.click(screen.getByRole('button', { name: 'Add channel' }));
    const dialog = screen.getByRole('dialog', { name: 'Add channel' });
    expect(within(dialog).getByRole('option', { name: /E-mail \(needs outgoing mail settings\)/ })).toBeDisabled();
    await user.type(within(dialog).getByLabelText('Name'), 'ops hook');
    await user.type(within(dialog).getByLabelText('URL'), 'https://hooks.example.test/T0/B0/x');
    await user.click(within(dialog).getByRole('button', { name: 'Add header' }));
    await user.type(within(dialog).getByLabelText('Header 1 name'), 'Authorization');
    await user.type(within(dialog).getByLabelText('Header 1 value'), 'Bearer t0ken');
    await user.click(within(dialog).getByLabelText('Sign requests (X-Pulsix-Signature-256)'));
    await user.type(within(dialog).getByLabelText('Signing secret (16–256 characters)'), '0123456789abcdef0123');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() =>
      expect(posted).toEqual({
        kind: 'webhook', name: 'ops hook', enabled: true,
        webhook: {
          method: 'POST', url: 'https://hooks.example.test/T0/B0/x', headers: [{ name: 'Authorization', value: 'Bearer t0ken' }],
          body_template: '', signed: true, signing_secret: '0123456789abcdef0123',
        },
      }),
    );
  });

  it('editing leaves stored secrets out of the request', async () => {
    const user = userEvent.setup();
    let put: { body: unknown; ifMatch: string | null } | undefined;
    mockApi({
      'GET /api/v1/auth/me': me,
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/notification-channels': { channels: [hook], mail_available: true },
      'PUT /api/v1/orgs/acme/notification-channels/c1': (req: MockRequest) => {
        put = { body: req.body, ifMatch: req.headers.get('If-Match') };
        return { channel: hook };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Edit ops hook' }));
    const dialog = screen.getByRole('dialog', { name: 'Edit ops hook' });
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(put?.ifMatch).toBe('"2"'));
    expect(put?.body).toEqual({
      kind: 'webhook', name: 'ops hook', enabled: true,
      webhook: { method: 'POST', headers: [{ name: 'Authorization' }], body_template: '', signed: true },
    });
  });

  it('reports the outcome of Send test', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me': me,
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/notification-channels': { channels: [hook], mail_available: true },
      'POST /api/v1/orgs/acme/notification-channels/c1/test': { delivery: { id: 'd', event_kind: 'test', status: 'failed', attempts: 1, last_error: 'the endpoint answered 404', created_at: '' } },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Send test through ops hook' }));
    expect(await screen.findByText('Test through ops hook failed: the endpoint answered 404')).toBeInTheDocument();
  });
});
