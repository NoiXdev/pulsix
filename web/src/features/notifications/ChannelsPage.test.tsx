import { describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { ChannelsPage } from './ChannelsPage.tsx';

const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });
const me = { user: { id: 'u1', email: 'a@acme.io', display_name: 'A' }, mfa_complete: true, mfa_enrolment_required: false, totp_confirmed: true, instance_admin: false };
const hook = {
  id: 'c1', kind: 'webhook', name: 'ops hook', enabled: true, revision: 2, created_at: '', updated_at: '',
  webhook: { method: 'POST', url: 'https://hooks.example.test/…', headers: ['Authorization'], signed: true },
};
const mailCh = { id: 'c2', kind: 'email', name: 'ops mail', enabled: false, revision: 1, created_at: '', updated_at: '', email: { recipients: ['ops@example.test'] } };

function renderPage(qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const page = createRoute({ getParentRoute: () => orgRoute, path: '/settings/notifications', component: ChannelsPage });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([page])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/settings/notifications'] }),
  });
  render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
  return qc;
}

/** Opens a row's ⋯ menu and picks an item. */
async function pick(user: ReturnType<typeof userEvent.setup>, channel: string, item: string) {
  await user.click(await screen.findByRole('button', { name: `Actions for ${channel}` }));
  await user.click(screen.getByRole('menuitem', { name: item }));
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
    expect(screen.getByRole('button', { name: 'Actions for ops hook' })).toBeInTheDocument();
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
    await pick(user, 'ops hook', 'Deliveries');
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
    expect(await screen.findByText('E-mail is not set up on this installation')).toBeInTheDocument();
    expect(screen.getByText(/outgoing mail settings/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Add channel' }));
    const dialog = screen.getByRole('dialog', { name: 'Add channel' });
    // Picking the disabled E-mail row must be a no-op (Select.test.tsx's
    // own "skips a disabled option" idiom, which proves it via onChange —
    // there is no `disabled` HTML attribute a custom combobox row carries
    // for toBeDisabled() to find). The combobox stays open with nothing
    // chosen (Select.tsx's onSelectedItemChange returns early on a
    // disabled item before closing), so the proof is that kind never
    // flipped to email: the URL field, shown only for a webhook, is still
    // there once the list is closed again.
    await user.click(within(dialog).getByLabelText('Kind'));
    await user.click(within(dialog).getByRole('option', { name: /E-mail \(needs outgoing mail settings\)/ }));
    await user.keyboard('{Escape}');
    expect(within(dialog).getByLabelText('URL')).toBeInTheDocument();
    await user.type(within(dialog).getByLabelText('Name'), 'ops hook');
    await user.type(within(dialog).getByLabelText('URL'), 'https://hooks.example.test/T0/B0/x');
    await user.click(within(dialog).getByRole('button', { name: 'Add header' }));
    await user.type(within(dialog).getByLabelText('Header 1 name'), 'Authorization');
    await user.type(within(dialog).getByLabelText('Header 1 value'), 'Bearer t0ken');
    await user.click(within(dialog).getByLabelText('Sign requests (X-Pulsix-Signature-256)'));
    await user.type(within(dialog).getByLabelText('Signing secret (16–256 characters)'), '0123456789abcdef0123');
    await user.click(within(dialog).getByRole('button', { name: 'Create channel' }));
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
    await pick(user, 'ops hook', 'Edit');
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
    await pick(user, 'ops hook', 'Send test');
    expect(await screen.findByText('Test through ops hook failed: the endpoint answered 404')).toBeInTheDocument();
  });

  it('holds back Send test only on the channel whose test is in flight', async () => {
    const user = userEvent.setup();
    let release: () => void = () => undefined;
    let posts = 0;
    mockApi({
      'GET /api/v1/auth/me': me,
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/notification-channels': { channels: [hook, mailCh], mail_available: true },
      'POST /api/v1/orgs/acme/notification-channels/c1/test': async () => {
        posts++;
        await new Promise<void>((resolve) => (release = resolve));
        return { delivery: { id: 'd', event_kind: 'test', status: 'sent', attempts: 1, created_at: '' } };
      },
    });
    renderPage();
    await pick(user, 'ops hook', 'Send test');
    await waitFor(() => expect(posts).toBe(1));

    await user.click(screen.getByRole('button', { name: 'Actions for ops hook' }));
    expect(await screen.findByRole('menuitem', { name: 'Sending test' })).toHaveAttribute('aria-disabled', 'true');
    await user.keyboard('{Escape}');

    await user.click(screen.getByRole('button', { name: 'Actions for ops mail' }));
    const other = await screen.findByRole('menuitem', { name: 'Send test' });
    expect(other).not.toHaveAttribute('aria-disabled');
    await user.keyboard('{Escape}');

    release();
    expect(await screen.findByText('Test message sent through ops hook')).toBeInTheDocument();
  });

  it('edits the weekly digest', async () => {
    const user = userEvent.setup();
    let put: unknown;
    mockApi({
      'GET /api/v1/auth/me': me,
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/notification-channels': { channels: [hook, mailCh], mail_available: true },
      'GET /api/v1/orgs/acme/notification-digest': { digest: { enabled: false, weekday: 'mon', time: '08:00', timezone: 'UTC', channels: [] } },
      'PUT /api/v1/orgs/acme/notification-digest': (req: MockRequest) => {
        put = req.body;
        return { digest: { enabled: true, weekday: 'fri', time: '08:00', timezone: 'UTC', channels: [{ id: 'c1', name: 'ops hook', kind: 'webhook' }], next_run_at: '2026-09-25T08:00:00Z' } };
      },
    });
    renderPage();
    const card = await screen.findByRole('region', { name: 'Weekly digest' });
    await user.click(within(card).getByLabelText('Send the weekly digest'));
    await pickOption(within(card), 'Day', { value: 'fri' });
    await user.click(within(card).getByLabelText('ops hook'));
    await user.click(within(card).getByRole('button', { name: 'Save digest' }));
    await waitFor(() => expect(put).toEqual({ enabled: true, weekday: 'fri', time: '08:00', timezone: 'UTC', channels: ['c1'] }));
    expect(await screen.findByText('Weekly digest saved')).toBeInTheDocument();
  });
});

/**
 * Backlog 2.85: ChannelsPage guarded `orgInfo` but not the channel list
 * itself, so a 5xx (or any other refusal) left `channels.data` undefined
 * forever and the page on its permanent "Loading…" skeleton — the same
 * silent-failure class the calendar's empty grid was, fixed the same way
 * (useQueryGuard renders the error state; DigestCard's own tests live in
 * DigestCard.test.tsx).
 */
describe('a failed channel-list query (backlog 2.85)', () => {
  it('reports the failure instead of loading forever', async () => {
    mockApi({
      'GET /api/v1/auth/me': me,
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/notification-channels': () =>
        new Response(JSON.stringify({ detail: 'internal error' }), { status: 500, headers: { 'Content-Type': 'application/json' } }),
    });
    renderPage();
    expect(await screen.findByText('Something went wrong')).toBeInTheDocument();
    expect(screen.getByText('internal error')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Add channel' })).not.toBeInTheDocument();
  });

  it('renders the list again once a refetch succeeds', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let failing = true;
    mockApi({
      'GET /api/v1/auth/me': me,
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/notification-channels': () =>
        failing
          ? new Response(JSON.stringify({ detail: 'internal error' }), { status: 500, headers: { 'Content-Type': 'application/json' } })
          : { channels: [hook], mail_available: true },
    });
    renderPage(qc);
    expect(await screen.findByText('Something went wrong')).toBeInTheDocument();
    failing = false;
    await qc.refetchQueries();
    expect(await screen.findByText('ops hook')).toBeInTheDocument();
    expect(screen.queryByText('Something went wrong')).not.toBeInTheDocument();
  });
});

/**
 * Backlog 2.75: the log showed status, attempts and the last error. An
 * operator looking at a failed row could neither see what had been
 * sent nor put it back on the queue once the receiver was fixed — the
 * message was simply gone, and the only recovery was to make the
 * underlying event happen again.
 */
describe('the delivery log can show a message and send it again', () => {
  const failed = { id: 'd1', event_kind: 'run.finished', status: 'failed', attempts: 6, last_error: 'the endpoint answered 503', created_at: '2026-09-22T08:00:00Z' };
  const sent = { id: 'd2', event_kind: 'test', status: 'sent', attempts: 1, created_at: '2026-09-22T07:00:00Z' };
  const payload = { kind: 'run.finished', org: { slug: 'acme' }, run: { result: 'failed' } };

  function openLog(role: string, extra: Record<string, unknown> = {}) {
    mockApi({
      'GET /api/v1/auth/me': me,
      'GET /api/v1/orgs/acme': org(role),
      'GET /api/v1/orgs/acme/notification-channels': { channels: [hook], mail_available: true },
      'GET /api/v1/orgs/acme/notification-channels/c1/deliveries': { deliveries: [failed, sent] },
      'GET /api/v1/orgs/acme/notification-channels/c1/deliveries/d1': { delivery: failed, payload },
      ...extra,
    });
    renderPage();
  }

  it('shows the event a message was built from', async () => {
    const user = userEvent.setup();
    openLog('operator');
    await screen.findByText('ops hook');
    await pick(user, 'ops hook', 'Deliveries');
    const dialog = await screen.findByRole('dialog', { name: 'Deliveries · ops hook' });
    const row = (await within(dialog).findByText(/the endpoint answered 503/)).closest('[data-delivery]') as HTMLElement;
    await user.click(within(row).getByRole('button', { name: 'Show message' }));
    const shown = await within(row).findByLabelText('Message sent');
    expect(shown).toHaveTextContent('"kind": "run.finished"');
    await user.click(within(row).getByRole('button', { name: 'Hide message' }));
    expect(within(row).queryByLabelText('Message sent')).toBeNull();
  });

  it('offers Send again on a failed delivery, to admins only', async () => {
    const user = userEvent.setup();
    let resent = 0;
    openLog('admin', {
      'POST /api/v1/orgs/acme/notification-channels/c1/deliveries/d1/resend': () => {
        resent++;
        return { delivery: { ...failed, status: 'pending', attempts: 0, last_error: '' } };
      },
    });
    await screen.findByText('ops hook');
    await pick(user, 'ops hook', 'Deliveries');
    const dialog = await screen.findByRole('dialog', { name: 'Deliveries · ops hook' });
    const failedRow = (await within(dialog).findByText(/the endpoint answered 503/)).closest('[data-delivery]') as HTMLElement;
    const sentRow = within(dialog).getByText('test').closest('[data-delivery]') as HTMLElement;
    // Only the failed one: a sent delivery would be a second copy in
    // front of a receiver that already has it.
    expect(within(sentRow).queryByRole('button', { name: 'Send again' })).toBeNull();
    await user.click(within(failedRow).getByRole('button', { name: 'Send again' }));
    await waitFor(() => expect(resent).toBe(1));
    expect(await screen.findByText('Queued to be sent again')).toBeInTheDocument();
  });

  it('does not offer it to an operator, who may read the log but not send', async () => {
    const user = userEvent.setup();
    openLog('operator');
    await screen.findByText('ops hook');
    await pick(user, 'ops hook', 'Deliveries');
    const dialog = await screen.findByRole('dialog', { name: 'Deliveries · ops hook' });
    await within(dialog).findByText(/the endpoint answered 503/);
    expect(within(dialog).queryByRole('button', { name: 'Send again' })).toBeNull();
  });
});
