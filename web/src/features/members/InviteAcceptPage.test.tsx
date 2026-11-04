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

const anonymous = () => new Response(JSON.stringify({ detail: 'login required' }), { status: 401 });

const signedInAs = (email: string) => ({
  user: { id: 'u9', email, display_name: 'Existing User' },
  mfa_complete: true,
  mfa_enrolment_required: false,
  totp_confirmed: true,
  instance_admin: false,
});

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
      // Anonymous until accepting opens the new account's session, which
      // must enrol a second factor (see tenancy.Service.AcceptInvite /
      // api.registerInviteAcceptRoute).
      'GET /api/v1/auth/me': () =>
        accepted === undefined
          ? anonymous()
          : {
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

  // Backlog 7.10: the link alone no longer joins an address that already
  // has an account; the server answers 409 and the invitation stays open.
  it('asks the holder of an existing account to sign in and open the link again', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mockApi({
      'GET /api/v1/auth/me': anonymous,
      'POST /api/v1/invites/accept': () =>
        new Response(JSON.stringify({ detail: 'this address already has an account' }), { status: 409 }),
    });

    renderPage(qc);
    await fillAndSubmit(user);

    expect(await screen.findByText('You already have an account')).toBeInTheDocument();
    expect(screen.getByText(/open this invitation link again/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Go to sign in' }));
    expect(await screen.findByText('LOGIN')).toBeInTheDocument();
  });

  it('a signed-in account joins with the token alone', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let accepted: unknown;
    mockApi({
      'GET /api/v1/auth/me': signedInAs('existing@acme.io'),
      'POST /api/v1/invites/accept': (req: MockRequest) => {
        accepted = req.body;
        return { user_id: 'u9', org_id: 'o1', signed_in: false };
      },
    });

    renderPage(qc);
    expect(await screen.findByText(/existing@acme\.io/)).toBeInTheDocument();
    expect(screen.queryByLabelText('Choose a password')).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Join organization' }));

    expect(await screen.findByText('ROOT')).toBeInTheDocument();
    expect(accepted).toEqual({ token: 'tok_abc123' });
  });

  it('tells a signed-in account whose address the invitation is not for what to do', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mockApi({
      'GET /api/v1/auth/me': signedInAs('someone-else@acme.io'),
      'POST /api/v1/invites/accept': () =>
        new Response(JSON.stringify({ detail: 'this address already has an account' }), { status: 409 }),
    });

    renderPage(qc);
    await user.click(await screen.findByRole('button', { name: 'Join organization' }));

    expect(await screen.findByText(/this address already has an account/)).toBeInTheDocument();
    expect(screen.getByText(/sign out and open this invitation link again/)).toBeInTheDocument();
  });

  // I4: the page used to treat *any* failure loading the new session as
  // "you already have an account". A new account whose session could not
  // be loaded (a 500 here) is told what actually happened instead.
  it('reports a failure to load the new session as such, not as an existing account', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let accepted = false;
    mockApi({
      'POST /api/v1/invites/accept': () => {
        accepted = true;
        return { user_id: 'u4', org_id: 'o1', signed_in: true };
      },
      'GET /api/v1/auth/me': () =>
        accepted ? new Response(JSON.stringify({ detail: 'internal error' }), { status: 500 }) : anonymous(),
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
      'GET /api/v1/auth/me': anonymous,
      'POST /api/v1/invites/accept': () => new Response(JSON.stringify({ detail: 'invitation expired' }), { status: 410 }),
    });

    renderPage(qc);
    await fillAndSubmit(user);

    expect(await screen.findByText('invitation expired')).toBeInTheDocument();
  });

  // The floor is the product's one password floor, 12 characters (backlog
  // 7.74); it used to be 8 here.
  it('refuses a password under 12 characters before asking the server', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let posted = false;
    mockApi({
      'GET /api/v1/auth/me': anonymous,
      'POST /api/v1/invites/accept': () => {
        posted = true;
        return { user_id: 'u2', org_id: 'o1', signed_in: true };
      },
    });

    renderPage(qc);
    await user.type(await screen.findByLabelText('Your name'), 'Ops User');
    const password = screen.getByLabelText('Choose a password');
    expect(password).toHaveAttribute('minLength', '12');
    await user.type(password, 'eleven-char');
    await user.click(screen.getByRole('button', { name: 'Accept invitation' }));

    expect(await screen.findByText('at least 12 characters')).toBeInTheDocument();
    expect(posted).toBe(false);
  });
});
