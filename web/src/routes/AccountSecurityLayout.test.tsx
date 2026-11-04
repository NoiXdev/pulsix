import { describe, expect, it } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../ui/toast.tsx';
import { mockApi } from '../test/mockApi.ts';
import { AccountSecurityLayout } from './AccountSecurityLayout.tsx';

function renderAt(initialPath = '/account/security', qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
  const rootRoute = createRootRoute();
  const securityRoute = createRoute({ getParentRoute: () => rootRoute, path: '/account/security', component: AccountSecurityLayout });
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
  const routeTree = rootRoute.addChildren([securityRoute, loginRoute, enrolRoute, passwordRoute]);
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [initialPath] }) });

  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

const ORGS = { orgs: [{ id: 'o1', slug: 'acme', name: 'Acme Hosting', role: 'owner', created_at: '2026-01-01T00:00:00Z' }] };

describe('AccountSecurityLayout', () => {
  it('renders inside the shell with Account / Security breadcrumbs and the real security page', async () => {
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
    expect(screen.getByText('Account')).toBeInTheDocument();
    expect(screen.getByText('second factor active')).toBeInTheDocument();
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
});

