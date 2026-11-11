import { describe, expect, it } from 'vitest';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../ui/toast.tsx';
import { mockApi } from '../test/mockApi.ts';
import { OrgLayout } from '../routes/OrgLayout.tsx';
import { HostsListPage } from '../features/hosts/HostsListPage.tsx';
import { LoginPage } from '../features/auth/LoginPage.tsx';
import { resetSessionCache, useSignOut } from './session.ts';
import { ConfirmProvider } from '../ui/confirm.tsx';

describe('resetSessionCache', () => {
  it('drops every cached query and every cached mutation', async () => {
    const qc = new QueryClient();
    qc.setQueryData(['me'], { user: { id: 'u1' } });
    qc.setQueryData(['hosts', 'acme', {}], { pages: [{ hosts: [{ id: 'h1' }] }], pageParams: [''] });
    // A finished mutation stays in the mutation cache (with its
    // variables) until garbage-collected — e.g. a TOTP code.
    await qc
      .getMutationCache()
      .build(qc, { mutationFn: (code: string) => Promise.resolve(code) })
      .execute('123456');
    expect(qc.getMutationCache().getAll()).toHaveLength(1);

    resetSessionCache(qc);

    expect(qc.getQueryCache().getAll()).toHaveLength(0);
    expect(qc.getMutationCache().getAll()).toHaveLength(0);
  });
});

describe('useSignOut', () => {
  it('ends the session on the server, lands on /login and drops the cache', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    qc.setQueryData(['me'], { user: { id: 'u1' } });
    const calls = mockApi({ 'POST /api/v1/auth/logout': {} });
    calls.mockClear();

    function SignOutButton() {
      const signOut = useSignOut();
      return (
        <button type="button" onClick={() => void signOut()}>
          Sign out
        </button>
      );
    }
    const rootRoute = createRootRoute();
    const pageRoute = createRoute({ getParentRoute: () => rootRoute, path: '/page', component: SignOutButton });
    const loginRoute = createRoute({
      getParentRoute: () => rootRoute,
      path: '/login',
      validateSearch: (s: Record<string, unknown>) => ({ step: typeof s.step === 'string' ? s.step : undefined }),
      component: () => <div>LOGIN PAGE</div>,
    });
    const router = createRouter({
      routeTree: rootRoute.addChildren([pageRoute, loginRoute]),
      history: createMemoryHistory({ initialEntries: ['/page'] }),
    });
    render(
      <QueryClientProvider client={qc}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    );

    await user.click(await screen.findByRole('button', { name: 'Sign out' }));

    expect(await screen.findByText('LOGIN PAGE')).toBeInTheDocument();
    const requested = calls.mock.calls.map(([input]) => {
      const req = input as Request;
      return `${req.method} ${new URL(req.url).pathname}`;
    });
    expect(requested).toContain('POST /api/v1/auth/logout');
    await waitFor(() => expect(qc.getQueryCache().getAll()).toHaveLength(0));
  });
});

const ORG_ACME = { id: 'o1', slug: 'acme', name: 'Acme Hosting', role: 'owner', created_at: '2026-01-01T00:00:00Z' };
const ALICE_HOST = {
  id: 'h1',
  name: 'alice-secret.fra',
  zone_id: 'z1',
  address: '203.0.113.4',
  managed: true,
  monitored: false,
  last_seen_at: '2026-03-01T00:00:00Z',
  facts: {},
};
const forbidden = () => new Response(JSON.stringify({ detail: 'forbidden' }), { status: 403 });

function renderApp(qc: QueryClient) {
  const rootRoute = createRootRoute();
  const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: '/', component: () => <div>ROOT</div> });
  const loginRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/login',
    validateSearch: (s: Record<string, unknown>) => ({ step: typeof s.step === 'string' ? s.step : undefined }),
    component: LoginPage,
  });
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org', component: OrgLayout });
  const hostsRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/hosts',
    validateSearch: (s: Record<string, unknown>) => ({ q: typeof s.q === 'string' ? s.q : undefined }),
    component: HostsListPage,
  });
  const routeTree = rootRoute.addChildren([indexRoute, loginRoute, orgRoute.addChildren([hostsRoute])]);
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ['/o/acme/hosts'] }) });

  render(
    <QueryClientProvider client={qc}>
      <ConfirmProvider>
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </ConfirmProvider>
    </QueryClientProvider>,
  );
  return router;
}

/**
 * I3: logout used to invalidate only ['me'], query keys do not include
 * the user, and useQueryGuard kept stale data on a failed refetch — so a
 * second user signing in on the same tab and pressing Back was shown the
 * first user's host list.
 */
describe('signing out, then signing in as someone else on the same tab (I3)', () => {
  it('drops the first user\'s data on sign-out and never renders it for the second user, not even on Back', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });

    // Alice's session.
    mockApi({
      'GET /api/v1/auth/me': {
        user: { id: 'u-alice', email: 'alice@acme.io', display_name: 'Alice' },
        mfa_complete: true,
        totp_confirmed: true,
        instance_admin: false,
      },
      'GET /api/v1/orgs': { orgs: [ORG_ACME] },
      'GET /api/v1/orgs/acme': { org: ORG_ACME },
      'GET /api/v1/orgs/acme/hosts': { hosts: [ALICE_HOST], next_cursor: '' },
      'GET /api/v1/orgs/acme/zones': { zones: [{ id: 'z1', slug: 'fra', name: 'Frankfurt' }] },
      'POST /api/v1/auth/logout': {},
    });

    const router = renderApp(qc);
    await screen.findByRole('link', { name: 'alice-secret.fra' });

    await user.click(screen.getByRole('button', { name: 'Account menu' }));
    await user.click(screen.getByRole('button', { name: 'Log out' }));

    await screen.findByLabelText('Email address');
    await waitFor(() => expect(qc.getQueryCache().getAll()).toHaveLength(0));

    // Bob signs in on the same tab; he is not a member of acme.
    mockApi({
      'POST /api/v1/auth/login': { mfa_required: false },
      'GET /api/v1/auth/me': {
        user: { id: 'u-bob', email: 'bob@example.org', display_name: 'Bob' },
        mfa_complete: true,
        totp_confirmed: true,
        instance_admin: false,
      },
      'GET /api/v1/orgs': { orgs: [] },
      'GET /api/v1/orgs/acme': forbidden,
      'GET /api/v1/orgs/acme/hosts': forbidden,
      'GET /api/v1/orgs/acme/zones': forbidden,
    });

    // Records whether Alice's host name is ever rendered from here on —
    // even for a single frame served from a stale cache.
    let leaked = false;
    const observer = new MutationObserver(() => {
      if (document.body.textContent?.includes('alice-secret.fra')) leaked = true;
    });
    observer.observe(document.body, { childList: true, subtree: true, characterData: true });

    await user.type(screen.getByLabelText('Email address'), 'bob@example.org');
    await user.type(screen.getByLabelText('Password'), 'correct horse battery staple');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    await screen.findByText('ROOT');

    // Back twice: / -> /login -> Alice's /o/acme/hosts.
    act(() => router.history.go(-2));

    expect(await screen.findByText('You do not have access')).toBeInTheDocument();
    observer.disconnect();
    expect(leaked).toBe(false);
    expect(screen.queryByRole('link', { name: 'alice-secret.fra' })).not.toBeInTheDocument();
  });
});
