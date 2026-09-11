import { describe, expect, it } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../ui/toast.tsx';
import { mockApi } from '../test/mockApi.ts';
import { fetchMock } from '../test/fetchMock.ts';
import { EnrolmentLayout } from './EnrolmentLayout.tsx';

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
});
