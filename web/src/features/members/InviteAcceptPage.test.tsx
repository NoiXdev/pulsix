import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { InviteAcceptPage } from './InviteAcceptPage.tsx';

function renderPage(qc: QueryClient) {
  const rootRoute = createRootRoute();
  const inviteRoute = createRoute({ getParentRoute: () => rootRoute, path: '/invite/$token', component: InviteAcceptPage });
  const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: '/', component: () => <div>ROOT</div> });
  const accountSecurityRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/account/enrol',
    component: () => <div>ENROLMENT</div>,
  });
  const loginRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/login',
    validateSearch: (search: Record<string, unknown>) => ({
      step: typeof search.step === 'string' ? search.step : undefined,
    }),
    component: () => <div>LOGIN</div>,
  });
  const routeTree = rootRoute.addChildren([indexRoute, accountSecurityRoute, loginRoute, inviteRoute]);
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ['/invite/tok_abc123'] }) });

  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

async function fillAndSubmit(user: ReturnType<typeof userEvent.setup>) {
  await user.type(await screen.findByLabelText('Your name'), 'Ops User');
  await user.type(screen.getByLabelText('Choose a password'), 'correct horse battery');
  await user.click(screen.getByRole('button', { name: 'Accept invitation' }));
}

describe('InviteAcceptPage', () => {
  it('a new invitee continues signed in towards second-factor enrolment', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let accepted: unknown;
    mockApi({
      'POST /api/v1/invites/accept': (req: MockRequest) => {
        accepted = req.body;
        return { user_id: 'u2', org_id: 'o1' };
      },
      // Accepting an invite that creates a new account opens an
      // MFA-complete session (see tenancy.Service.AcceptInvite /
      // api.registerInviteAcceptRoute), so the follow-up /auth/me here
      // succeeds — this is what tells the page it does not need to send
      // the invitee to /login.
      'GET /api/v1/auth/me': {
        user: { id: 'u2', email: 'ops@acme.io', display_name: 'Ops User' },
        mfa_complete: true,
        totp_confirmed: false,
        instance_admin: false,
      },
    });

    renderPage(qc);
    await fillAndSubmit(user);

    expect(await screen.findByText('ENROLMENT')).toBeInTheDocument();
    expect(accepted).toMatchObject({ token: 'tok_abc123', display_name: 'Ops User' });
  });

  it('an existing account is told to sign in with its own password instead of being left signed in', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mockApi({
      'POST /api/v1/invites/accept': () => ({ user_id: 'u3', org_id: 'o1' }),
      // No session was opened (the address already had its own password),
      // so /auth/me correctly 401s — the page must read that as "go sign
      // in", not as an error accepting the invite.
      'GET /api/v1/auth/me': () => new Response(JSON.stringify({ detail: 'no session' }), { status: 401 }),
    });

    renderPage(qc);
    await fillAndSubmit(user);

    expect(await screen.findByText('You already have an account')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Go to sign in' }));
    expect(await screen.findByText('LOGIN')).toBeInTheDocument();
  });

  it('shows an inline error instead of an unhandled rejection when the token is invalid', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mockApi({
      'POST /api/v1/invites/accept': () => new Response(JSON.stringify({ detail: 'invitation expired' }), { status: 410 }),
    });

    renderPage(qc);
    await fillAndSubmit(user);

    expect(await screen.findByText('invitation expired')).toBeInTheDocument();
  });
});
