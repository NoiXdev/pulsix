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
        return { user_id: 'u2', org_id: 'o1', signed_in: true };
      },
      // Accepting an invite that creates a new account opens a session
      // that must enrol a second factor (see tenancy.Service.AcceptInvite /
      // api.registerInviteAcceptRoute).
      'GET /api/v1/auth/me': {
        user: { id: 'u2', email: 'ops@acme.io', display_name: 'Ops User' },
        mfa_complete: false,
        mfa_enrolment_required: true,
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
      // No session was opened: the address already had its own password.
      'POST /api/v1/invites/accept': () => ({ user_id: 'u3', org_id: 'o1', signed_in: false }),
    });

    renderPage(qc);
    await fillAndSubmit(user);

    expect(await screen.findByText('You already have an account')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Go to sign in' }));
    expect(await screen.findByText('LOGIN')).toBeInTheDocument();
  });

  // I4: the page used to treat *any* failure loading the new session as
  // "you already have an account". A new account whose session could not
  // be loaded (a 500 here) is told what actually happened instead.
  it('reports a failure to load the new session as such, not as an existing account', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mockApi({
      'POST /api/v1/invites/accept': () => ({ user_id: 'u4', org_id: 'o1', signed_in: true }),
      'GET /api/v1/auth/me': () => new Response(JSON.stringify({ detail: 'internal error' }), { status: 500 }),
    });

    renderPage(qc);
    await fillAndSubmit(user);

    expect(await screen.findByText(/signing you in failed: internal error/)).toBeInTheDocument();
    expect(screen.queryByText('You already have an account')).not.toBeInTheDocument();
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
