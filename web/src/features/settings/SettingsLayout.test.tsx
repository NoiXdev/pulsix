import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { mockApi } from '../../test/mockApi.ts';
import { SettingsLayout } from './SettingsLayout.tsx';

function renderLayout(path = '/o/acme/settings/members') {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const settingsRoute = createRoute({ getParentRoute: () => orgRoute, path: '/settings', component: SettingsLayout });
  const page = (p: string) =>
    createRoute({ getParentRoute: () => settingsRoute, path: p, component: () => <div>page {p}</div> });
  const leaves = ['/general', '/members', '/activity', '/tokens', '/notifications', '/mail'].map(page);
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([settingsRoute.addChildren(leaves)])]),
    history: createMemoryHistory({ initialEntries: [path] }),
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

const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });

describe('SettingsLayout', () => {
  it('shows a viewer the Organization group only', async () => {
    mockApi({ 'GET /api/v1/auth/me': me(false), 'GET /api/v1/orgs/acme': org('viewer') });
    renderLayout();
    expect(await screen.findByRole('link', { name: 'General' })).toHaveAttribute('href', '/o/acme/settings/general');
    expect(screen.getByRole('link', { name: 'Members' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Activity' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'API tokens' })).toBeInTheDocument();
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByText('Alerting')).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Notifications' })).not.toBeInTheDocument();
    expect(screen.queryByText('Instance')).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Outgoing mail' })).not.toBeInTheDocument();
  });

  it('shows an operator the Alerting group as well', async () => {
    mockApi({ 'GET /api/v1/auth/me': me(false), 'GET /api/v1/orgs/acme': org('operator') });
    renderLayout();
    expect(await screen.findByText('Alerting')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Notifications' })).toHaveAttribute('href', '/o/acme/settings/notifications');
    expect(screen.getByRole('link', { name: 'Alert routes' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'On-call rotations' })).toBeInTheDocument();
    expect(screen.queryByText('Instance')).not.toBeInTheDocument();
  });

  it('shows an instance administrator the Instance group as well', async () => {
    mockApi({ 'GET /api/v1/auth/me': me(true), 'GET /api/v1/orgs/acme': org('viewer') });
    renderLayout();
    expect(await screen.findByText('Instance')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Outgoing mail' })).toHaveAttribute('href', '/o/acme/settings/mail');
    expect(screen.getByRole('link', { name: 'Language' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Backups' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Shared resources' })).toHaveAttribute(
      'href',
      '/o/acme/settings/shared-resources',
    );
    expect(screen.queryByText('Alerting')).not.toBeInTheDocument();
  });

  it('marks the item of the current route and renders the page beside it', async () => {
    mockApi({ 'GET /api/v1/auth/me': me(false), 'GET /api/v1/orgs/acme': org('viewer') });
    renderLayout('/o/acme/settings/activity');
    expect(await screen.findByRole('link', { name: 'Activity' })).toHaveAttribute('aria-current', 'page');
    expect(screen.getByRole('link', { name: 'Members' })).not.toHaveAttribute('aria-current');
    expect(screen.getByText('page /activity')).toBeInTheDocument();
  });
});
