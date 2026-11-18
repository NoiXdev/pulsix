import { afterEach, describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../ui/toast.tsx';
import { mockApi } from '../test/mockApi.ts';
import { AccountLayout } from './AccountLayout.tsx';
import { AccountSecurityPage } from '../features/account/AccountSecurityPage.tsx';
import { rememberOrg } from '../lib/lastOrg.ts';

function renderAt(initialPath = '/account/security', qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
  const rootRoute = createRootRoute();
  const accountRoute = createRoute({ getParentRoute: () => rootRoute, path: '/account', component: AccountLayout });
  const securityRoute = createRoute({ getParentRoute: () => accountRoute, path: '/security', component: AccountSecurityPage });
  const appearanceRoute = createRoute({ getParentRoute: () => accountRoute, path: '/appearance', component: () => <div>APPEARANCE PAGE</div> });
  const hostsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/o/$org/hosts',
    validateSearch: (s: Record<string, unknown>) => ({ q: typeof s.q === 'string' ? s.q : undefined }),
    component: () => <div>HOSTS PAGE</div>,
  });
  const loginRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/login',
    validateSearch: (s: Record<string, unknown>) => ({ step: typeof s.step === 'string' ? s.step : undefined }),
    component: () => <div>LOGIN PAGE</div>,
  });
  const enrolRoute = createRoute({ getParentRoute: () => rootRoute, path: '/account/enrol', component: () => <div>ENROLMENT</div> });
  const passwordRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/account/password',
    component: () => <div>CHANGE PASSWORD</div>,
  });
  const routeTree = rootRoute.addChildren([accountRoute.addChildren([securityRoute, appearanceRoute]), hostsRoute, loginRoute, enrolRoute, passwordRoute]);
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [initialPath] }) });

  const result = render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
  return { ...result, router };
}

const ORGS = { orgs: [{ id: 'o1', slug: 'acme', name: 'Acme Hosting', role: 'owner', created_at: '2026-01-01T00:00:00Z' }] };
const TWO_ORGS = {
  orgs: [
    { id: 'o1', slug: 'acme', name: 'Acme', role: 'owner', created_at: '2026-01-01T00:00:00Z' },
    { id: 'o2', slug: 'beta', name: 'Beta', role: 'viewer', created_at: '2026-01-01T00:00:00Z' },
  ],
};
const ME = {
  user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
  mfa_complete: true,
  totp_confirmed: true,
  instance_admin: true,
};

afterEach(() => sessionStorage.clear());

describe('AccountLayout', () => {
  it('renders inside the shell with My account / Security breadcrumbs and the real security page', async () => {
    mockApi({
      'GET /api/v1/auth/me': {
        user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
        mfa_complete: true,
        totp_confirmed: true,
        instance_admin: true,
      },
      'GET /api/v1/orgs': ORGS,
      'GET /api/v1/auth/passkeys': { passkeys: [] },
    });

    renderAt();

    expect(await screen.findByRole('heading', { name: 'Security' })).toBeInTheDocument();
    expect(within(await screen.findByRole('region', { name: 'Sign-in' })).getByText('Second factor active')).toBeInTheDocument();
    const crumbs = screen.getByRole('banner');
    expect(within(crumbs).getByText('My account')).toBeInTheDocument();
    expect(within(crumbs).getByText('Security')).toBeInTheDocument();
  });

  it('shows no organization, alerting or instance links in the account sidebar', async () => {
    mockApi({
      'GET /api/v1/auth/me': {
        user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
        mfa_complete: true,
        totp_confirmed: true,
        instance_admin: true,
      },
      'GET /api/v1/orgs': ORGS,
      'GET /api/v1/auth/passkeys': { passkeys: [] },
    });
    renderAt();
    const nav = await screen.findByRole('navigation', { name: 'My account' });
    expect(within(nav).queryByRole('group')).not.toBeInTheDocument();
    expect(within(nav).queryByRole('link', { name: 'Members' })).not.toBeInTheDocument();
    expect(within(nav).queryByRole('link', { name: 'Backups' })).not.toBeInTheDocument();
  });

  it('sends a session whose account has no second factor yet to enrolment, without requesting organizations', async () => {
    let orgsRequested = false;
    mockApi({
      'GET /api/v1/auth/me': {
        user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
        mfa_complete: false,
        mfa_enrolment_required: true,
        totp_confirmed: false,
        instance_admin: false,
      },
      'GET /api/v1/orgs': () => {
        orgsRequested = true;
        return ORGS;
      },
      'GET /api/v1/auth/passkeys': { passkeys: [] },
    });

    renderAt();

    expect(await screen.findByText('ENROLMENT')).toBeInTheDocument();
    expect(orgsRequested).toBe(false);
  });

  it('sends a session whose account must change its password to /account/password, without requesting organizations', async () => {
    let orgsRequested = false;
    mockApi({
      'GET /api/v1/auth/me': {
        user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
        mfa_complete: true,
        mfa_enrolment_required: false,
        totp_confirmed: true,
        instance_admin: false,
        must_change_password: true,
      },
      'GET /api/v1/orgs': () => {
        orgsRequested = true;
        return ORGS;
      },
      'GET /api/v1/auth/passkeys': { passkeys: [] },
    });

    renderAt();

    expect(await screen.findByText('CHANGE PASSWORD')).toBeInTheDocument();
    expect(orgsRequested).toBe(false);
  });

  it('shows a friendly page instead of hanging when the account belongs to no organization', async () => {
    mockApi({
      'GET /api/v1/auth/me': {
        user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
        mfa_complete: true,
        totp_confirmed: false,
        instance_admin: true,
      },
      'GET /api/v1/orgs': { orgs: [] },
    });

    renderAt();

    expect(await screen.findByText(/does not belong to any organization/)).toBeInTheDocument();
  });

  it('logs out to /login and clears the whole query cache (I3)', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    qc.setQueryData(['hosts', 'acme', {}], { pages: [{ hosts: [{ id: 'h1' }] }], pageParams: [''] });
    mockApi({
      'GET /api/v1/auth/me': {
        user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
        mfa_complete: true,
        totp_confirmed: true,
        instance_admin: true,
      },
      'GET /api/v1/orgs': ORGS,
      'GET /api/v1/auth/passkeys': { passkeys: [] },
      'POST /api/v1/auth/logout': {},
    });

    renderAt('/account/security', qc);

    await user.click(await screen.findByRole('button', { name: 'Account menu' }));
    await user.click(screen.getByRole('button', { name: 'Log out' }));

    expect(await screen.findByText('LOGIN PAGE')).toBeInTheDocument();
    await waitFor(() => expect(qc.getQueryCache().getAll()).toHaveLength(0));
  });

  it('shows the settings navigation: back link, name and role, the five pages and no organization switcher', async () => {
    mockApi({ 'GET /api/v1/auth/me': ME, 'GET /api/v1/orgs': TWO_ORGS, 'GET /api/v1/auth/passkeys': { passkeys: [] } });
    renderAt();

    const nav = await screen.findByRole('navigation', { name: 'My account' });
    expect(within(nav).getByRole('link', { name: 'Back to Acme' })).toHaveAttribute('href', '/o/acme/hosts');
    expect(within(nav).getByText('Tim R.')).toBeInTheDocument();
    expect(within(nav).getByText('Owner · Acme')).toBeInTheDocument();
    const mine = nav;
    expect(within(mine).getAllByRole('link').map((l) => l.textContent)).toEqual(['Back to Acme', 'Security', 'Appearance', 'Profile', 'API tokens', 'My activity']);
    expect(within(mine).getByRole('link', { name: 'Security' })).toHaveAttribute('aria-current', 'page');
    expect(within(mine).getByRole('link', { name: 'API tokens' })).toHaveAttribute('href', '/account/tokens');
    expect(screen.queryByRole('combobox', { name: 'Organization' })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Hosts' })).not.toBeInTheDocument();
  });

  it('goes back to the organization the person came from, with their role there', async () => {
    rememberOrg('beta');
    mockApi({ 'GET /api/v1/auth/me': ME, 'GET /api/v1/orgs': TWO_ORGS, 'GET /api/v1/auth/passkeys': { passkeys: [] } });
    renderAt();

    const nav = await screen.findByRole('navigation', { name: 'My account' });
    expect(within(nav).getByRole('link', { name: 'Back to Beta' })).toHaveAttribute('href', '/o/beta/hosts');
    expect(within(nav).getByText('Viewer · Beta')).toBeInTheDocument();
  });

  it('falls back to the first organization when the remembered one is not one of theirs', async () => {
    rememberOrg('gone');
    mockApi({ 'GET /api/v1/auth/me': ME, 'GET /api/v1/orgs': TWO_ORGS, 'GET /api/v1/auth/passkeys': { passkeys: [] } });
    renderAt();

    expect(await screen.findByRole('link', { name: 'Back to Acme' })).toBeInTheDocument();
  });

  it('follows the navigation to the other pages and names them in the breadcrumbs', async () => {
    const user = userEvent.setup();
    mockApi({ 'GET /api/v1/auth/me': ME, 'GET /api/v1/orgs': TWO_ORGS, 'GET /api/v1/auth/passkeys': { passkeys: [] } });
    renderAt();

    await user.click(await screen.findByRole('link', { name: 'Appearance' }));
    expect(await screen.findByText('APPEARANCE PAGE')).toBeInTheDocument();
    expect(within(screen.getByRole('banner')).getByText('Appearance')).toBeInTheDocument();
  });
});
