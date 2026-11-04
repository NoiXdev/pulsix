import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  createRootRoute,
  createRoute,
  createRouter,
  createMemoryHistory,
  RouterProvider,
  redirect,
} from '@tanstack/react-router';
import { mockApi } from '../../test/mockApi.ts';
import { RootRedirect } from '../../routes/RootRedirect.tsx';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import type { Locale } from '../../i18n/types.ts';
import { ToastProvider } from '../../ui/toast.tsx';
import { LoginPage } from './LoginPage.tsx';

/**
 * A small end-to-end router (not the app's real one, which also pulls in
 * every other page) exercising exactly the path finding "Important 3"
 * names: a pending session lands on /login?step=totp via RootRedirect, a
 * correct code is submitted, and the app must land on the organisation
 * page — not bounce back to the TOTP prompt because the ['me'] query
 * cache was still holding the pre-verification, MFA-pending snapshot.
 */
function buildRouter(qc: QueryClient) {
  const rootRoute = createRootRoute();
  const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: '/', component: RootRedirect });
  const loginRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/login',
    validateSearch: (search: Record<string, unknown>) => ({
      step: typeof search.step === 'string' ? search.step : undefined,
    }),
    component: LoginPage,
  });
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const hostsRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/hosts',
    validateSearch: (search: Record<string, unknown>) => ({
      q: typeof search.q === 'string' ? search.q : undefined,
    }),
    component: function HostsStub() {
      return <div>ORGANIZATION PAGE</div>;
    },
  });
  const orgIndexRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/',
    beforeLoad: ({ params }) => {
      throw redirect({ to: '/o/$org/hosts', params: { org: params.org }, search: { q: undefined } });
    },
  });

  const routeTree = rootRoute.addChildren([
    indexRoute,
    loginRoute,
    orgRoute.addChildren([orgIndexRoute, hostsRoute]),
  ]);
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ['/'] }) });

  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

/** renderLoginAt mounts just LoginPage behind a minimal router — enough
 * for useNavigate/useSearch to work — for tests that don't need
 * RootRedirect's own logic. */
function renderLoginAt(
  search: { step?: string } = {},
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } }),
  locale?: Locale,
) {
  const rootRoute = createRootRoute();
  const loginRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/login',
    validateSearch: (s: Record<string, unknown>) => ({ step: typeof s.step === 'string' ? s.step : undefined }),
    component: LoginPage,
  });
  const indexRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/',
    component: () => <div>ROOT</div>,
  });
  const routeTree = rootRoute.addChildren([indexRoute, loginRoute]);
  const initial = search.step ? `/login?step=${search.step}` : '/login';
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [initial] }) });

  const tree = (
    <ToastProvider>
      <RouterProvider router={router} />
    </ToastProvider>
  );
  render(<QueryClientProvider client={qc}>{locale ? <I18nProvider locale={locale}>{tree}</I18nProvider> : tree}</QueryClientProvider>);
}

describe('LoginPage forgotten password (backlog 7.74)', () => {
  it('links to /forgot-password from the password step', async () => {
    renderLoginAt();

    expect(await screen.findByRole('link', { name: 'Forgot password?' })).toHaveAttribute('href', '/forgot-password');
  });
});

describe('LoginPage TOTP step (Important 5)', () => {
  it('offers "back" to the password step', async () => {
    const user = userEvent.setup();
    mockApi({
      'POST /api/v1/auth/login': { mfa_required: true },
    });
    renderLoginAt();

    await user.type(await screen.findByLabelText('Email address'), 'tim@acme.io');
    await user.type(screen.getByLabelText('Password'), 'correct horse battery staple');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));

    await screen.findByLabelText('6-digit code');
    await user.click(screen.getByRole('button', { name: 'Back to sign in' }));

    expect(await screen.findByLabelText('Email address')).toBeInTheDocument();
  });

  it('offers a recovery-code path that posts recovery_code instead of code — the recovery codes the app hands out were otherwise unusable to sign in', async () => {
    const user = userEvent.setup();
    const verify = mockApi({
      'POST /api/v1/auth/totp/verify': {},
      'GET /api/v1/auth/me': {
        user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
        mfa_complete: true,
        totp_confirmed: true,
        instance_admin: false,
      },
    });
    renderLoginAt({ step: 'totp' });

    await user.click(await screen.findByRole('button', { name: 'Use a recovery code' }));
    await user.type(screen.getByLabelText('Recovery code'), 'r3cov-3ry-c0d3');
    await user.click(screen.getByRole('button', { name: 'Verify' }));

    await screen.findByText('ROOT');

    const call = verify.mock.calls.find(([req]) => {
      const url = req instanceof Request ? req.url : req.toString();
      return url.includes('/auth/totp/verify');
    });
    expect(call).toBeDefined();
    const sentBody = call?.[0] instanceof Request ? await call[0].clone().json() : undefined;
    expect(sentBody).toEqual({ recovery_code: 'r3cov-3ry-c0d3' });
  });

  it('offers a passkey option when the browser supports WebAuthn, so a passkey-only user who authenticated with a password is not stuck', async () => {
    const originalPKC = window.PublicKeyCredential;
    const originalCredentials = navigator.credentials;
    Object.defineProperty(window, 'PublicKeyCredential', { value: class {}, configurable: true });
    Object.defineProperty(navigator, 'credentials', { value: {}, configurable: true });

    renderLoginAt({ step: 'totp' });
    expect(await screen.findByRole('button', { name: /Use a passkey instead/ })).toBeInTheDocument();

    Object.defineProperty(window, 'PublicKeyCredential', { value: originalPKC, configurable: true });
    Object.defineProperty(navigator, 'credentials', { value: originalCredentials, configurable: true });
  });
});

describe('LoginPage password step error handling (cheap fix)', () => {
  it('shows a toast instead of an unhandled rejection for a non-401 failure (e.g. a 500)', async () => {
    const user = userEvent.setup();
    mockApi({
      'POST /api/v1/auth/login': () => new Response(JSON.stringify({ detail: 'internal error' }), { status: 500 }),
    });
    renderLoginAt();

    await user.type(await screen.findByLabelText('Email address'), 'tim@acme.io');
    await user.type(screen.getByLabelText('Password'), 'correct horse battery staple');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));

    const toast = await screen.findByRole('status');
    expect(toast).toHaveTextContent('internal error');
  });
});

describe('LoginPage in German', () => {
  it('shows the form and the wrong-password message in German', async () => {
    const user = userEvent.setup();
    mockApi({
      'POST /api/v1/auth/login': () => new Response(JSON.stringify({ detail: 'invalid credentials' }), { status: 401 }),
    });
    renderLoginAt({}, undefined, 'de');

    await user.type(await screen.findByLabelText('E-Mail-Adresse'), 'tim@acme.io');
    await user.type(screen.getByLabelText('Passwort'), 'wrong');
    await user.click(screen.getByRole('button', { name: 'Anmelden' }));

    expect(await screen.findByText('ungültige Anmeldedaten')).toBeInTheDocument();
  });
});

describe('a pending session completing verification (Important 3)', () => {
  it('walks a pending session through a successful TOTP verification and ends on the organisation page, not back on the prompt', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });

    // Flips once POST /auth/totp/verify succeeds — models the backend's
    // own session state, which the SPA has no way to observe except by
    // re-fetching /auth/me.
    let mfaComplete = false;

    mockApi({
      'GET /api/v1/setup/status': { completed: true },
      'GET /api/v1/auth/me': () => ({
        user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
        mfa_complete: mfaComplete,
        totp_confirmed: mfaComplete,
        instance_admin: false,
      }),
      'GET /api/v1/orgs': { orgs: [{ id: 'org1', slug: 'acme', name: 'Acme Hosting', role: 'owner', created_at: '2026-01-01T00:00:00Z' }] },
      'POST /api/v1/auth/totp/verify': () => {
        mfaComplete = true;
        return {};
      },
    });

    buildRouter(qc);

    // RootRedirect resolves the pending session and sends us to the TOTP
    // step (arriving here already proves the "no session" and "setup
    // incomplete" branches were correctly skipped).
    await screen.findByLabelText('6-digit code');

    await user.type(screen.getByLabelText('6-digit code'), '482913');
    await user.click(screen.getByRole('button', { name: 'Verify' }));

    // Before the fix: the stale cached mfa_complete: false sent this
    // straight back to the TOTP prompt. After: the org page.
    expect(await screen.findByText('ORGANIZATION PAGE')).toBeInTheDocument();
    expect(screen.queryByLabelText('6-digit code')).not.toBeInTheDocument();
  });
});

/** I3: a new session must start from an empty cache — nothing an earlier
 * principal loaded on this tab may survive a sign-in. */
describe('LoginPage session reset (I3)', () => {
  it('clears every cached query before re-fetching /auth/me, so only the new session\'s data remains', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    qc.setQueryData(['hosts', 'acme', {}], { pages: [{ hosts: [{ id: 'h1', name: 'alice-secret.fra' }] }], pageParams: [''] });
    qc.setQueryData(['me'], { user: { id: 'u-alice' }, mfa_complete: true });
    mockApi({
      'POST /api/v1/auth/login': { mfa_required: false },
      'GET /api/v1/auth/me': {
        user: { id: 'u-bob', email: 'bob@example.org', display_name: 'Bob' },
        mfa_complete: true,
        totp_confirmed: true,
        instance_admin: false,
      },
    });
    renderLoginAt({}, qc);

    await user.type(await screen.findByLabelText('Email address'), 'bob@example.org');
    await user.type(screen.getByLabelText('Password'), 'correct horse battery staple');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    await screen.findByText('ROOT');

    expect(qc.getQueryCache().findAll({ queryKey: ['hosts'] })).toHaveLength(0);
    expect(qc.getQueryData(['me'])).toMatchObject({ user: { id: 'u-bob' } });
  });
});
