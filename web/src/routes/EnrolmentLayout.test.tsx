import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../ui/toast.tsx';
import { mockApi } from '../test/mockApi.ts';
import { fetchMock } from '../test/fetchMock.ts';
import { registerPasskey } from '../features/auth/passkeys.ts';
import { EnrolmentLayout } from './EnrolmentLayout.tsx';
import { RootLayout } from './RootLayout.tsx';
import { RootRedirect } from './RootRedirect.tsx';

// The WebAuthn ceremony itself is the browser's; what is under test is what
// the page does once it succeeded.
vi.mock('../features/auth/passkeys.ts', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../features/auth/passkeys.ts')>()),
  registerPasskey: vi.fn(),
}));

const USER = { id: 'u1', email: 'new@acme.io', display_name: 'New Person' };
const ENROLLING = { user: USER, mfa_complete: false, mfa_enrolment_required: true, totp_confirmed: false, instance_admin: false };
const COMPLETE = { user: USER, mfa_complete: true, mfa_enrolment_required: false, totp_confirmed: true, instance_admin: false };

function renderAt(qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
  const rootRoute = createRootRoute();
  const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: '/', component: () => <div>ROOT</div> });
  const enrolRoute = createRoute({ getParentRoute: () => rootRoute, path: '/account/enrol', component: EnrolmentLayout });
  const loginRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/login',
    validateSearch: (s: Record<string, unknown>) => ({ step: typeof s.step === 'string' ? s.step : undefined }),
    component: () => <div>LOGIN PAGE</div>,
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([indexRoute, enrolRoute, loginRoute]),
    history: createMemoryHistory({ initialEntries: ['/account/enrol'] }),
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

describe('EnrolmentLayout (a second factor is mandatory)', () => {
  it('offers enrolment without the app shell and never asks for organizations', async () => {
    mockApi({ 'GET /api/v1/auth/me': ENROLLING, 'GET /api/v1/auth/passkeys': { passkeys: [] } });

    renderAt();

    expect(await screen.findByRole('heading', { name: 'Set up a second factor' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Enrol authenticator app' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Add passkey/ })).toBeInTheDocument();
    const requested = fetchMock.mock.calls.map(([input]) => new URL((input as Request).url).pathname);
    expect(requested.some((p) => p.startsWith('/api/v1/orgs'))).toBe(false);
  });

  it('keeps the recovery codes on screen after the session completes, then continues into the app once they are saved', async () => {
    const user = userEvent.setup();
    let confirmed = false;
    mockApi({
      'GET /api/v1/auth/me': () => (confirmed ? COMPLETE : ENROLLING),
      'GET /api/v1/auth/passkeys': { passkeys: [] },
      'POST /api/v1/auth/totp/enrol': { secret: 'JBSWY3DPEHPK3PXP', otpauth_url: 'otpauth://totp/pulsix?secret=JBSWY3DPEHPK3PXP' },
      'POST /api/v1/auth/totp/confirm': () => {
        confirmed = true;
        return { recovery_codes: ['aaaa-bbbb', 'cccc-dddd'] };
      },
    });

    renderAt();
    await user.click(await screen.findByRole('button', { name: 'Enrol authenticator app' }));
    await user.type(await screen.findByPlaceholderText('482913'), '123456');
    await user.click(screen.getByRole('button', { name: 'Confirm' }));

    expect(await screen.findByText('aaaa-bbbb')).toBeInTheDocument();
    // /auth/me now reports a complete session (useConfirmTotp invalidates
    // it); the codes must survive that refetch.
    await waitFor(() => expect(screen.getByText('second factor active')).toBeInTheDocument());
    expect(screen.getByText('aaaa-bbbb')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'I have saved these' }));
    expect(await screen.findByText('ROOT')).toBeInTheDocument();
  });

  it('sends a session with nothing to enrol on to the root redirect', async () => {
    mockApi({ 'GET /api/v1/auth/me': COMPLETE, 'GET /api/v1/auth/passkeys': { passkeys: [] } });

    renderAt();

    expect(await screen.findByText('ROOT')).toBeInTheDocument();
  });

  // A cached /auth/me from before the enrolment still says a factor is
  // missing; the page waits for the refetch on mount instead of deciding
  // on it, so it never shows the enrolment to a complete session.
  it('decides on a fresh /auth/me, not on a stale cached one', async () => {
    mockApi({ 'GET /api/v1/auth/me': COMPLETE, 'GET /api/v1/auth/passkeys': { passkeys: [] } });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    qc.setQueryData(['me'], ENROLLING);
    let shown = false;
    const observer = new MutationObserver(() => {
      if (document.body.textContent?.includes('Set up a second factor')) shown = true;
    });
    observer.observe(document.body, { childList: true, subtree: true, characterData: true });

    renderAt(qc);

    expect(await screen.findByText('ROOT')).toBeInTheDocument();
    observer.disconnect();
    expect(shown).toBe(false);
  });

  it('signs out to /login and clears the cache', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me': ENROLLING,
      'GET /api/v1/auth/passkeys': { passkeys: [] },
      'POST /api/v1/auth/logout': {},
    });

    const qc = renderAt();
    await user.click(await screen.findByRole('button', { name: /Sign out/ }));

    expect(await screen.findByText('LOGIN PAGE')).toBeInTheDocument();
    await waitFor(() => expect(qc.getQueryCache().getAll()).toHaveLength(0));
  });

  // After an administrator reset the second factor and set a temporary
  // password, the person signs in with the password, enrols, and only then
  // changes the password. Registering the passkey completes the session,
  // and from that moment the passkey list is refused with
  // password_change_required: the page must neither break nor loop on that
  // refusal, but carry on to the password change.
  it('carries on to the password change when the account must change its password, through the refused passkey refetch', async () => {
    const user = userEvent.setup();
    let complete = false;
    let passkeyRequests = 0;
    vi.mocked(registerPasskey).mockImplementation(async () => {
      complete = true;
    });
    mockApi({
      'GET /api/v1/setup/status': { completed: true },
      'GET /api/v1/auth/me': () => ({ ...(complete ? COMPLETE : ENROLLING), must_change_password: true }),
      'GET /api/v1/auth/passkeys': () => {
        passkeyRequests += 1;
        return complete
          ? new Response(JSON.stringify({ title: 'Forbidden', status: 403, detail: 'password_change_required' }), { status: 403 })
          : { passkeys: [] };
      },
    });

    const rootRoute = createRootRoute({ component: RootLayout });
    const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: '/', component: RootRedirect });
    const enrolRoute = createRoute({ getParentRoute: () => rootRoute, path: '/account/enrol', component: EnrolmentLayout });
    const passwordRoute = createRoute({
      getParentRoute: () => rootRoute,
      path: '/account/password',
      component: () => <div>CHANGE PASSWORD</div>,
    });
    const router = createRouter({
      routeTree: rootRoute.addChildren([indexRoute, enrolRoute, passwordRoute]),
      history: createMemoryHistory({ initialEntries: ['/account/enrol'] }),
    });
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </QueryClientProvider>,
    );

    await user.type(await screen.findByPlaceholderText('e.g. MacBook Pro'), 'Laptop');
    await user.click(screen.getByRole('button', { name: /Add passkey/ }));

    expect(await screen.findByText('CHANGE PASSWORD')).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/account/password');
    expect(passkeyRequests).toBeLessThanOrEqual(2);
  });

  // The same forced flow through the authenticator app: confirming
  // completes the session while the recovery codes are still on screen.
  // From then on the API refuses every enrolment route until the password
  // is changed, so the page stops offering a passkey; saving the codes
  // carries on to the change.
  it('stops offering a passkey once a TOTP confirm completes a session that must change its password', async () => {
    const user = userEvent.setup();
    let confirmed = false;
    mockApi({
      'GET /api/v1/setup/status': { completed: true },
      'GET /api/v1/auth/me': () => ({ ...(confirmed ? COMPLETE : ENROLLING), must_change_password: true }),
      'GET /api/v1/auth/passkeys': () =>
        confirmed
          ? new Response(JSON.stringify({ title: 'Forbidden', status: 403, detail: 'password_change_required' }), { status: 403 })
          : { passkeys: [] },
      'POST /api/v1/auth/totp/enrol': { secret: 'JBSWY3DPEHPK3PXP', otpauth_url: 'otpauth://totp/pulsix?secret=JBSWY3DPEHPK3PXP' },
      'POST /api/v1/auth/totp/confirm': () => {
        confirmed = true;
        return { recovery_codes: ['aaaa-bbbb', 'cccc-dddd'] };
      },
    });

    const rootRoute = createRootRoute({ component: RootLayout });
    const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: '/', component: RootRedirect });
    const enrolRoute = createRoute({ getParentRoute: () => rootRoute, path: '/account/enrol', component: EnrolmentLayout });
    const passwordRoute = createRoute({
      getParentRoute: () => rootRoute,
      path: '/account/password',
      component: () => <div>CHANGE PASSWORD</div>,
    });
    const router = createRouter({
      routeTree: rootRoute.addChildren([indexRoute, enrolRoute, passwordRoute]),
      history: createMemoryHistory({ initialEntries: ['/account/enrol'] }),
    });
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </QueryClientProvider>,
    );

    await user.click(await screen.findByRole('button', { name: 'Enrol authenticator app' }));
    await user.type(await screen.findByPlaceholderText('482913'), '123456');
    await user.click(screen.getByRole('button', { name: 'Confirm' }));

    expect(await screen.findByText('aaaa-bbbb')).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText('second factor active')).toBeInTheDocument());
    expect(screen.queryByRole('button', { name: /Add passkey/ })).not.toBeInTheDocument();
    expect(screen.getByText('aaaa-bbbb')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'I have saved these' }));
    expect(await screen.findByText('CHANGE PASSWORD')).toBeInTheDocument();
  });
});
