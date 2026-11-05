import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { useMe } from '../../api/hooks/useMe.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { mockApi } from '../../test/mockApi.ts';
import { SettingsLayout } from './SettingsLayout.tsx';

/** Shows what the two gates answered, from the same queries the layout
 * reads, so a test can wait for those answers on screen before it counts
 * what they leave out. */
function GatesAnswered() {
  const me = useMe();
  const org = useOrg('acme');
  if (!me.data || !org.data) return null;
  return <p>{`gates: ${me.data.instance_admin ? 'instance admin' : 'member'}, ${org.data.org.role}`}</p>;
}

function renderLayout(path = '/o/acme/settings/members') {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const settingsRoute = createRoute({ getParentRoute: () => orgRoute, path: '/settings', component: SettingsLayout });
  const page = (p: string) =>
    createRoute({
      getParentRoute: () => settingsRoute,
      path: p,
      component: () => (
        <div>
          page {p}
          <GatesAnswered />
        </div>
      ),
    });
  const leaves = ['/general', '/members', '/activity', '/tokens', '/notifications', '/mail', '/shared/repos', '/shared/repos/$id', '/users', '/users/$userId', '/organizations'].map(page);
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([settingsRoute.addChildren(leaves)])]),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return qc;
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
    // Both gates have answered (a viewer, not an instance admin) before the absences count.
    expect(await screen.findByText('gates: member, viewer')).toBeInTheDocument();
    expect(screen.queryByText('Alerting')).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Notifications' })).not.toBeInTheDocument();
    expect(screen.queryByText('Instance')).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Outgoing mail' })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Organizations' })).not.toBeInTheDocument();
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
    const users = screen.getByRole('link', { name: 'Users' });
    expect(users).toHaveAttribute('href', '/o/acme/settings/users');
    // Users leads the Instance group, Organizations follows it.
    const organizations = screen.getByRole('link', { name: 'Organizations' });
    expect(organizations).toHaveAttribute('href', '/o/acme/settings/organizations');
    const links = screen.getAllByRole('link');
    expect(links[links.indexOf(users) + 1]).toBe(organizations);
    expect(links[links.indexOf(organizations) + 1]).toHaveAccessibleName('Outgoing mail');
    expect(links[links.indexOf(users) - 1]).toHaveAccessibleName('API tokens');
    expect(screen.getByRole('link', { name: 'Outgoing mail' })).toHaveAttribute('href', '/o/acme/settings/mail');
    expect(screen.getByRole('link', { name: 'Language' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Backups' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Shared repositories' })).toHaveAttribute(
      'href',
      '/o/acme/settings/shared/repos',
    );
    expect(screen.getByRole('link', { name: 'Shared job templates' })).toHaveAttribute(
      'href',
      '/o/acme/settings/shared/templates',
    );
    expect(screen.getByRole('link', { name: 'Shared check definitions' })).toHaveAttribute(
      'href',
      '/o/acme/settings/shared/checks',
    );
    expect(screen.queryByRole('link', { name: 'Shared resources' })).not.toBeInTheDocument();
    expect(screen.queryByText('Alerting')).not.toBeInTheDocument();
  });

  it('keeps the shared repositories item current on one of its own pages', async () => {
    mockApi({ 'GET /api/v1/auth/me': me(true), 'GET /api/v1/orgs/acme': org('viewer') });
    renderLayout('/o/acme/settings/shared/repos/d1');
    expect(await screen.findByRole('link', { name: 'Shared repositories' })).toHaveAttribute('aria-current', 'page');
    expect(screen.getByRole('link', { name: 'Shared job templates' })).not.toHaveAttribute('aria-current');
  });

  it('keeps the Users item current on an account page', async () => {
    mockApi({ 'GET /api/v1/auth/me': me(true), 'GET /api/v1/orgs/acme': org('viewer') });
    renderLayout('/o/acme/settings/users/u2');
    expect(await screen.findByRole('link', { name: 'Users' })).toHaveAttribute('aria-current', 'page');
  });

  it('marks the item of the current route and renders the page beside it', async () => {
    mockApi({ 'GET /api/v1/auth/me': me(false), 'GET /api/v1/orgs/acme': org('viewer') });
    renderLayout('/o/acme/settings/activity');
    expect(await screen.findByRole('link', { name: 'Activity' })).toHaveAttribute('aria-current', 'page');
    expect(screen.getByRole('link', { name: 'Members' })).not.toHaveAttribute('aria-current');
    expect(screen.getByText('page /activity')).toBeInTheDocument();
  });
});
