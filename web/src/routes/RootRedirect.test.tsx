import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  createRootRoute,
  createRoute,
  createRouter,
  createMemoryHistory,
  RouterProvider,
  useSearch,
} from '@tanstack/react-router';
import { mockApi } from '../test/mockApi.ts';
import { RootRedirect } from './RootRedirect.tsx';

function renderRoot() {
  const rootRoute = createRootRoute();
  const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: '/', component: RootRedirect });
  const setupRoute = createRoute({ getParentRoute: () => rootRoute, path: '/setup', component: () => <div>SETUP</div> });
  const loginRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/login',
    validateSearch: (s: Record<string, unknown>) => ({ step: typeof s.step === 'string' ? s.step : undefined }),
    component: function LoginStub() {
      const search = useSearch({ strict: false }) as { step?: string };
      return <div>LOGIN{search.step === 'totp' ? ' TOTP STEP' : ''}</div>;
    },
  });
  const enrolRoute = createRoute({ getParentRoute: () => rootRoute, path: '/account/enrol', component: () => <div>ENROLMENT</div> });
  const passwordRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/account/password',
    component: () => <div>CHANGE PASSWORD</div>,
  });
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const hostsRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/hosts',
    validateSearch: (s: Record<string, unknown>) => ({ q: typeof s.q === 'string' ? s.q : undefined }),
    component: () => <div>HOSTS PAGE</div>,
  });
  const routeTree = rootRoute.addChildren([indexRoute, setupRoute, loginRoute, enrolRoute, passwordRoute, orgRoute.addChildren([hostsRoute])]);
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ['/'] }) });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });

  render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

describe('RootRedirect', () => {
  it('redirects to /setup when setup is not completed', async () => {
    mockApi({ 'GET /api/v1/setup/status': { completed: false } });
    renderRoot();
    expect(await screen.findByText('SETUP')).toBeInTheDocument();
  });

  it('redirects to /login when there is no session', async () => {
    mockApi({
      'GET /api/v1/setup/status': { completed: true },
      'GET /api/v1/auth/me': () => new Response(JSON.stringify({ detail: 'login required' }), { status: 401 }),
    });
    renderRoot();
    expect(await screen.findByText('LOGIN')).toBeInTheDocument();
  });

  it('redirects to /login?step=totp when the session has a second factor to verify', async () => {
    mockApi({
      'GET /api/v1/setup/status': { completed: true },
      'GET /api/v1/auth/me': {
        user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
        mfa_complete: false,
        mfa_enrolment_required: false,
        totp_confirmed: true,
        instance_admin: false,
      },
    });
    renderRoot();
    expect(await screen.findByText('LOGIN TOTP STEP')).toBeInTheDocument();
  });

  it('redirects to /account/enrol when the account has no second factor yet (it is mandatory)', async () => {
    mockApi({
      'GET /api/v1/setup/status': { completed: true },
      'GET /api/v1/auth/me': {
        user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
        mfa_complete: false,
        mfa_enrolment_required: true,
        totp_confirmed: false,
        instance_admin: false,
      },
    });
    renderRoot();
    expect(await screen.findByText('ENROLMENT')).toBeInTheDocument();
  });

  it('redirects to /account/password when the account must change its password, without asking for organizations', async () => {
    const calls = mockApi({
      'GET /api/v1/setup/status': { completed: true },
      'GET /api/v1/auth/me': {
        user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
        mfa_complete: true,
        mfa_enrolment_required: false,
        totp_confirmed: true,
        instance_admin: false,
        must_change_password: true,
      },
    });
    calls.mockClear();
    renderRoot();
    expect(await screen.findByText('CHANGE PASSWORD')).toBeInTheDocument();
    const requested = calls.mock.calls.map(([input]) => new URL((input as Request).url).pathname);
    expect(requested).not.toContain('/api/v1/orgs');
  });

  it('redirects to the first organization for a ready session', async () => {
    mockApi({
      'GET /api/v1/setup/status': { completed: true },
      'GET /api/v1/auth/me': {
        user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
        mfa_complete: true,
        totp_confirmed: true,
        instance_admin: false,
      },
      'GET /api/v1/orgs': {
        orgs: [{ id: 'o1', slug: 'acme', name: 'Acme Hosting', role: 'owner', created_at: '2026-01-01T00:00:00Z' }],
      },
    });
    renderRoot();
    expect(await screen.findByText('HOSTS PAGE')).toBeInTheDocument();
  });

  // Important 7 (task-15a-report.md fix round 1): a ready session that
  // belongs to zero organizations used to wait on "Loading…" forever —
  // orgs.data resolves, there is no first org, and nothing else happened.
  it('shows a friendly message instead of hanging on "Loading…" for a session with zero organizations', async () => {
    mockApi({
      'GET /api/v1/setup/status': { completed: true },
      'GET /api/v1/auth/me': {
        user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
        mfa_complete: true,
        totp_confirmed: true,
        instance_admin: false,
      },
      'GET /api/v1/orgs': { orgs: [] },
    });
    renderRoot();
    expect(await screen.findByText(/does not belong to any organization/)).toBeInTheDocument();
    expect(screen.queryByText('Loading…')).not.toBeInTheDocument();
  });

  it('shows an error page instead of hanging when /orgs itself fails', async () => {
    mockApi({
      'GET /api/v1/setup/status': { completed: true },
      'GET /api/v1/auth/me': {
        user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
        mfa_complete: true,
        totp_confirmed: true,
        instance_admin: false,
      },
      'GET /api/v1/orgs': () => new Response(JSON.stringify({ detail: 'internal error' }), { status: 500 }),
    });
    renderRoot();
    expect(await screen.findByText('Something went wrong')).toBeInTheDocument();
  });
});
