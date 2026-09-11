import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../ui/toast.tsx';
import { mockApi } from '../test/mockApi.ts';
import { AccountSecurityLayout } from './AccountSecurityLayout.tsx';

function renderAt(initialPath = '/account/security') {
  const rootRoute = createRootRoute();
  const securityRoute = createRoute({ getParentRoute: () => rootRoute, path: '/account/security', component: AccountSecurityLayout });
  const routeTree = rootRoute.addChildren([securityRoute]);
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [initialPath] }) });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });

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
});
