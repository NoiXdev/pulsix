import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { mockApi } from '../test/mockApi.ts';
import { OrgLayout } from './OrgLayout.tsx';

const ME_OK = {
  user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
  mfa_complete: true,
  totp_confirmed: true,
  instance_admin: false,
};
const ORGS_OK = { orgs: [{ id: 'o1', slug: 'acme', name: 'Acme Hosting', role: 'owner', created_at: '2026-01-01T00:00:00Z' }] };
const ORG_OK = { org: { id: 'o1', slug: 'acme', name: 'Acme Hosting', role: 'owner', created_at: '2026-01-01T00:00:00Z' } };

function renderAt(initialPath: string) {
  const rootRoute = createRootRoute();
  const loginRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/login',
    validateSearch: (s: Record<string, unknown>) => ({ step: typeof s.step === 'string' ? s.step : undefined }),
    component: () => <div>LOGIN PAGE</div>,
  });
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org', component: OrgLayout });
  const membersRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/settings/members',
    component: () => <div>MEMBERS PAGE</div>,
  });
  const hostsRoute = createRoute({ getParentRoute: () => orgRoute, path: '/hosts', component: () => <div>HOSTS PAGE</div> });
  const routeTree = rootRoute.addChildren([loginRoute, orgRoute.addChildren([hostsRoute, membersRoute])]);
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [initialPath] }) });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });

  render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

describe('OrgLayout (Important 7 error handling + breadcrumbs)', () => {
  it('renders the page with breadcrumbs derived from the real route (Settings > Members)', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': ORG_OK,
      'GET /api/v1/auth/me': ME_OK,
      'GET /api/v1/orgs': ORGS_OK,
    });
    renderAt('/o/acme/settings/members');

    expect(await screen.findByText('MEMBERS PAGE')).toBeInTheDocument();
    const header = document.querySelector('header');
    expect(header).toHaveTextContent('SettingsMembers');
  });

  it('redirects to /login on a 401 instead of hanging on "Loading…"', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': () => new Response(JSON.stringify({ detail: 'login required' }), { status: 401 }),
      'GET /api/v1/auth/me': ME_OK,
      'GET /api/v1/orgs': ORGS_OK,
    });
    renderAt('/o/acme/hosts');

    expect(await screen.findByText('LOGIN PAGE')).toBeInTheDocument();
  });

  it('shows a friendly "no access" page on a 403 rather than hanging', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': () => new Response(JSON.stringify({ detail: 'forbidden' }), { status: 403 }),
      'GET /api/v1/auth/me': ME_OK,
      'GET /api/v1/orgs': ORGS_OK,
    });
    renderAt('/o/acme/hosts');

    expect(await screen.findByText('You do not have access')).toBeInTheDocument();
    expect(screen.queryByText('Loading…')).not.toBeInTheDocument();
  });
});
