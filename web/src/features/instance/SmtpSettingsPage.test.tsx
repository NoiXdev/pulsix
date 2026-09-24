import { describe, expect, it } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { SmtpSettingsPage } from './SmtpSettingsPage.tsx';

const ADMIN = {
  user: { id: 'u1', email: 'admin@acme.io', display_name: 'Ada' },
  mfa_complete: true,
  mfa_enrolment_required: false,
  totp_confirmed: true,
  instance_admin: true,
};

function renderPage() {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const mailRoute = createRoute({ getParentRoute: () => orgRoute, path: '/settings/mail', component: SmtpSettingsPage });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([mailRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/settings/mail'] }),
  });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('SmtpSettingsPage (I4: outgoing mail can be configured after setup)', () => {
  it('configures mail on an instance that skipped it in the wizard', async () => {
    const user = userEvent.setup();
    let saved: unknown;
    let configured = false;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/settings/smtp': () =>
        configured
          ? { configured: true, host: 'smtp.acme.io', port: 2525, username: 'mailer', from: 'pulsix@acme.io', start_tls: false }
          : { configured: false },
      'PUT /api/v1/admin/settings/smtp': (req: MockRequest) => {
        saved = req.body;
        configured = true;
        return new Response(null, { status: 204 });
      },
    });

    renderPage();
    expect(await screen.findByText('not configured')).toBeInTheDocument();
    expect(await screen.findByRole('tab', { name: 'Outgoing mail' })).toBeInTheDocument();

    await user.type(screen.getByLabelText('Host'), 'smtp.acme.io');
    await user.clear(screen.getByLabelText('Port'));
    await user.type(screen.getByLabelText('Port'), '2525');
    await user.type(screen.getByLabelText('Username'), 'mailer');
    await user.type(screen.getByLabelText('Password'), 's3cret');
    await user.type(screen.getByLabelText('From address'), 'pulsix@acme.io');
    await user.click(screen.getByLabelText('Use STARTTLS'));
    await user.click(screen.getByRole('button', { name: /Save mail settings/ }));

    expect(await screen.findByText('Mail settings saved')).toBeInTheDocument();
    expect(saved).toEqual({
      host: 'smtp.acme.io',
      port: 2525,
      username: 'mailer',
      password: 's3cret',
      from: 'pulsix@acme.io',
      start_tls: false,
    });
    expect(await screen.findByText('configured')).toBeInTheDocument();
    expect(screen.getByLabelText('Host')).toHaveValue('smtp.acme.io');
    // The stored password is never shown back.
    expect(screen.getByLabelText('Password')).toHaveValue('');
  });

  it('keeps the stored password when the field is left empty, and removes it on request', async () => {
    const user = userEvent.setup();
    const saves: Array<Record<string, unknown>> = [];
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/settings/smtp': {
        configured: true, host: 'smtp.acme.io', port: 2525, username: 'mailer', from: 'pulsix@acme.io', start_tls: false,
      },
      'PUT /api/v1/admin/settings/smtp': (req: MockRequest) => {
        saves.push(req.body as Record<string, unknown>);
        return new Response(null, { status: 204 });
      },
    });

    renderPage();
    expect(await screen.findByText('configured')).toBeInTheDocument();

    // Changing the host alone: the password field stays empty because the
    // page cannot show it, so the save must not carry one at all.
    await user.clear(screen.getByLabelText('Host'));
    await user.type(screen.getByLabelText('Host'), 'smtp2.acme.io');
    await user.click(screen.getByRole('button', { name: /Save mail settings/ }));
    expect(await screen.findByText('Mail settings saved')).toBeInTheDocument();
    expect(saves[0]).toMatchObject({ host: 'smtp2.acme.io' });
    expect(saves[0]).not.toHaveProperty('password');

    // Typing one replaces it.
    await user.type(screen.getByLabelText('Password'), 'n3w');
    await user.click(screen.getByRole('button', { name: /Save mail settings/ }));
    await waitFor(() => expect(saves).toHaveLength(2));
    expect(saves[1]).toMatchObject({ password: 'n3w' });

    // And the explicit checkbox clears it — the only way to reach a relay
    // that wants no password at all.
    await user.click(screen.getByLabelText('Remove the stored password'));
    await user.click(screen.getByRole('button', { name: /Save mail settings/ }));
    await waitFor(() => expect(saves).toHaveLength(3));
    expect(saves[2]).toMatchObject({ password: '' });
  });

  it('sends a test e-mail and shows the mail server\'s answer when it fails', async () => {
    const user = userEvent.setup();
    let attempts = 0;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/settings/smtp': { configured: true, host: 'smtp.acme.io', port: 587, from: 'pulsix@acme.io', start_tls: true },
      'POST /api/v1/admin/settings/smtp/test': () => {
        attempts += 1;
        return attempts === 1
          ? { sent: true }
          : new Response(JSON.stringify({ detail: '535 authentication failed' }), { status: 502 });
      },
    });

    renderPage();
    await user.type(await screen.findByLabelText('Recipient'), 'me@acme.io');
    await user.click(screen.getByRole('button', { name: /Send test e-mail/ }));
    expect(await screen.findByText('Test e-mail sent to me@acme.io')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: /Send test e-mail/ }));
    expect(await screen.findByText('535 authentication failed')).toBeInTheDocument();
  });

  it('rejects invalid input inline before saving', async () => {
    const user = userEvent.setup();
    let saves = 0;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/settings/smtp': { configured: false },
      'PUT /api/v1/admin/settings/smtp': () => {
        saves += 1;
        return new Response(null, { status: 204 });
      },
    });

    renderPage();
    await user.click(await screen.findByRole('button', { name: /Save mail settings/ }));

    expect(await screen.findByText('required')).toBeInTheDocument();
    expect(saves).toBe(0);
  });

  it('shows the access-denied page to someone who is not an instance administrator', async () => {
    mockApi({
      'GET /api/v1/auth/me': { ...ADMIN, instance_admin: false },
      'GET /api/v1/admin/settings/smtp': () =>
        new Response(JSON.stringify({ detail: 'instance admin required' }), { status: 403 }),
    });

    renderPage();

    expect(await screen.findByText('You do not have access')).toBeInTheDocument();
  });
});
