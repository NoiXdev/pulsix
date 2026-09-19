import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { mockApi } from '../test/mockApi.ts';
import { SettingsTabs } from './SettingsTabs.tsx';

function renderTabs() {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const membersRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/settings/members',
    component: () => <SettingsTabs org="acme" />,
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([membersRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/settings/members'] }),
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

const me = (instanceAdmin: boolean) => ({
  user: { id: 'u1', email: 'a@acme.io', display_name: 'A' },
  mfa_complete: true,
  mfa_enrolment_required: false,
  totp_confirmed: true,
  instance_admin: instanceAdmin,
});

describe('SettingsTabs', () => {
  it('shows the instance-wide "Outgoing mail" tab to an instance administrator', async () => {
    mockApi({ 'GET /api/v1/auth/me': me(true) });
    renderTabs();
    expect(await screen.findByRole('tab', { name: 'Outgoing mail' })).toBeInTheDocument();
  });

  it('links to the Credentials and Jump hosts tabs', async () => {
    mockApi({ 'GET /api/v1/auth/me': me(false) });
    renderTabs();
    expect(await screen.findByRole('tab', { name: 'Credentials' })).toHaveAttribute('href', '/o/acme/settings/credentials');
    expect(screen.getByRole('tab', { name: 'Jump hosts' })).toHaveAttribute('href', '/o/acme/settings/jump-hosts');
  });

  it('hides it from everyone else', async () => {
    mockApi({ 'GET /api/v1/auth/me': me(false) });
    renderTabs();
    expect(await screen.findByRole('tab', { name: 'Members' })).toBeInTheDocument();
    // Give /auth/me a chance to resolve before asserting absence.
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByRole('tab', { name: 'Outgoing mail' })).not.toBeInTheDocument();
  });

  it('shows the Notifications tab to operators and above', async () => {
    mockApi({
      'GET /api/v1/auth/me': me(false),
      'GET /api/v1/orgs/acme': { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } },
    });
    renderTabs();
    expect(await screen.findByRole('tab', { name: 'Notifications' })).toHaveAttribute('href', '/o/acme/settings/notifications');
  });

  it('hides the Notifications tab from viewers', async () => {
    mockApi({
      'GET /api/v1/auth/me': me(false),
      'GET /api/v1/orgs/acme': { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'viewer', created_at: '' } },
    });
    renderTabs();
    expect(await screen.findByRole('tab', { name: 'Members' })).toBeInTheDocument();
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByRole('tab', { name: 'Notifications' })).not.toBeInTheDocument();
  });
});
