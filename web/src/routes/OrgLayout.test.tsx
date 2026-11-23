import { afterEach, describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { I18nProvider } from '../i18n/I18nProvider.tsx';
import { ToastProvider } from '../ui/toast.tsx';
import { mockApi } from '../test/mockApi.ts';
import { lastOrg } from '../lib/lastOrg.ts';
import { OrgLayout } from './OrgLayout.tsx';

afterEach(() => sessionStorage.clear());

const ME_OK = {
  user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
  mfa_complete: true,
  totp_confirmed: true,
  instance_admin: false,
};
const ORGS_OK = { orgs: [{ id: 'o1', slug: 'acme', name: 'Acme Hosting', role: 'owner', created_at: '2026-01-01T00:00:00Z' }] };
const ORG_OK = { org: { id: 'o1', slug: 'acme', name: 'Acme Hosting', role: 'owner', created_at: '2026-01-01T00:00:00Z' } };

function renderAt(initialPath: string, locale?: 'de') {
  const rootRoute = createRootRoute();
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
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org', component: OrgLayout });
  const membersRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/settings/members',
    component: () => <div>MEMBERS PAGE</div>,
  });
  const generalRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/settings/general',
    component: () => <div>GENERAL PAGE</div>,
  });
  const hostsRoute = createRoute({ getParentRoute: () => orgRoute, path: '/hosts', component: () => <div>HOSTS PAGE</div> });
  const hostRoute = createRoute({ getParentRoute: () => orgRoute, path: '/hosts/$id', component: () => <div>HOST PAGE</div> });
  const groupRoute = createRoute({ getParentRoute: () => orgRoute, path: '/groups/$id', component: () => <div>GROUP PAGE</div> });
  const newPlaybookRoute = createRoute({ getParentRoute: () => orgRoute, path: '/playbooks/new', component: () => <div>NEW PLAYBOOK PAGE</div> });
  const playbookRoute = createRoute({ getParentRoute: () => orgRoute, path: '/playbooks/$id', component: () => <div>PLAYBOOK PAGE</div> });
  const routeTree = rootRoute.addChildren([
    loginRoute,
    enrolRoute,
    passwordRoute,
    orgRoute.addChildren([hostsRoute, hostRoute, groupRoute, newPlaybookRoute, playbookRoute, membersRoute, generalRoute]),
  ]);
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [initialPath] }) });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });

  render(
    <QueryClientProvider client={qc}>
      <I18nProvider locale={locale ?? 'en'}>
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe('OrgLayout (Important 7 error handling + breadcrumbs)', () => {
  it('shows the demo-mode strip in the notice slot while the second factor is optional', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': ORG_OK,
      'GET /api/v1/auth/me': { ...ME_OK, totp_confirmed: false, mfa_enrolment_required: false },
      'GET /api/v1/orgs': ORGS_OK,
      'GET /api/v1/setup/status': { completed: true, version: 'dev', default_locale: 'en', mfa_optional: true },
    });
    renderAt('/o/acme/hosts');

    expect(await screen.findByText('HOSTS PAGE')).toBeInTheDocument();
    expect(await screen.findByText('Demo mode: the second factor is optional on this instance.')).toBeInTheDocument();
  });

  it('remembers the organization, so the settings area can lead back to it', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': ORG_OK,
      'GET /api/v1/auth/me': ME_OK,
      'GET /api/v1/orgs': ORGS_OK,
    });
    renderAt('/o/acme/hosts');

    expect(await screen.findByText('HOSTS PAGE')).toBeInTheDocument();
    expect(lastOrg()).toBe('acme');
  });

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

  it('names the host in the breadcrumbs instead of its id', async () => {
    const id = '550e8400-e29b-41d4-a716-446655440000';
    mockApi({
      'GET /api/v1/orgs/acme': ORG_OK,
      'GET /api/v1/auth/me': ME_OK,
      'GET /api/v1/orgs': ORGS_OK,
      [`GET /api/v1/orgs/acme/hosts/${id}`]: { host: { id, name: 'web-04.fra', zone_id: 'z1', address: '', managed: false, monitored: false, revision: 1 } },
    });
    renderAt(`/o/acme/hosts/${id}`);

    expect(await screen.findByText('HOST PAGE')).toBeInTheDocument();
    const header = document.querySelector('header');
    await waitFor(() => expect(header).toHaveTextContent('Hostsweb-04.fra'));
  });

  it('names a group and its ancestors in the breadcrumbs', async () => {
    const summary = (id: string, name: string, parent_id?: string) => ({
      id, name, parent_id, description: '', members: 0, group_fields: 0, host_fields: 0, complete: true, revision: 1,
    });
    mockApi({
      'GET /api/v1/orgs/acme': ORG_OK,
      'GET /api/v1/auth/me': ME_OK,
      'GET /api/v1/orgs': ORGS_OK,
      'GET /api/v1/orgs/acme/inventory/groups': {
        groups: [summary('p1', 'prod'), summary('g1', 'web', 'p1'), summary('c1', 'web_eu', 'g1'), summary('x1', 'other')],
      },
    });
    renderAt('/o/acme/groups/c1');

    expect(await screen.findByText('GROUP PAGE')).toBeInTheDocument();
    const header = document.querySelector('header');
    await waitFor(() => expect(header).toHaveTextContent('Groupsprodwebweb_eu'));
    expect(within(header as HTMLElement).getByText('web_eu')).toHaveClass('font-semibold');
  });

  it('names a playbook in the breadcrumbs', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': ORG_OK,
      'GET /api/v1/auth/me': ME_OK,
      'GET /api/v1/orgs': ORGS_OK,
      'GET /api/v1/orgs/acme/playbooks': {
        playbooks: [
          { id: 'p1', source: 'editor', summary: 'Install nginx', created_at: '', updated_at: '' },
          { id: 'p2', source: 'repo', summary: '', path: 'site.yml', repo_id: 'r1', created_at: '', updated_at: '' },
        ],
      },
    });
    renderAt('/o/acme/playbooks/p2');
    expect(await screen.findByText('PLAYBOOK PAGE')).toBeInTheDocument();
    const header = document.querySelector('header');
    await waitFor(() => expect(header).toHaveTextContent('Playbookssite.yml'));
  });

  it('names the new playbook page in the breadcrumbs', async () => {
    mockApi({ 'GET /api/v1/orgs/acme': ORG_OK, 'GET /api/v1/auth/me': ME_OK, 'GET /api/v1/orgs': ORGS_OK });
    renderAt('/o/acme/playbooks/new');
    expect(await screen.findByText('NEW PLAYBOOK PAGE')).toBeInTheDocument();
    await waitFor(() => expect(document.querySelector('header')).toHaveTextContent('PlaybooksNew playbook'));
  });

  it('swaps the organization navigation for the settings navigation under /settings', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': ORG_OK,
      'GET /api/v1/auth/me': { ...ME_OK, instance_admin: true },
      'GET /api/v1/orgs': ORGS_OK,
    });
    renderAt('/o/acme/settings/general');

    expect(await screen.findByText('GENERAL PAGE')).toBeInTheDocument();
    const nav = screen.getByRole('navigation', { name: 'Settings' });
    expect(within(nav).getByRole('link', { name: 'Back to Acme Hosting' })).toBeInTheDocument();
    expect(within(nav).queryByRole('group', { name: 'My account' })).not.toBeInTheDocument();
    expect(within(nav).queryByRole('link', { name: 'Security' })).not.toBeInTheDocument();
    expect(within(nav).getByRole('group', { name: 'Organization · Acme Hosting' })).toBeInTheDocument();
    expect(within(nav).getByRole('group', { name: 'Alerting' })).toBeInTheDocument();
    expect(within(nav).getByRole('group', { name: 'Instance' })).toBeInTheDocument();
    expect(screen.queryByRole('combobox', { name: 'Organization' })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: /^Hosts/ })).not.toBeInTheDocument();
    expect(document.querySelector('header')).toHaveTextContent('SettingsGeneral');
  });

  it('shows a viewer only Organization, and keeps the organization navigation elsewhere', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': { org: { ...ORG_OK.org, role: 'viewer' } },
      'GET /api/v1/auth/me': ME_OK,
      'GET /api/v1/orgs': ORGS_OK,
    });
    renderAt('/o/acme/settings/general');

    const nav = await screen.findByRole('navigation', { name: 'Settings' });
    expect(within(nav).getByRole('group', { name: 'Organization · Acme Hosting' })).toBeInTheDocument();
    expect(within(nav).queryByRole('group', { name: 'Alerting' })).not.toBeInTheDocument();
    expect(within(nav).queryByRole('group', { name: 'Instance' })).not.toBeInTheDocument();
  });

  it('keeps the organization navigation on an ordinary page', async () => {
    mockApi({ 'GET /api/v1/orgs/acme': ORG_OK, 'GET /api/v1/auth/me': ME_OK, 'GET /api/v1/orgs': ORGS_OK });
    renderAt('/o/acme/hosts');
    expect(await screen.findByText('HOSTS PAGE')).toBeInTheDocument();
    expect(screen.getByRole('combobox', { name: 'Organization' })).toBeInTheDocument();
    expect(screen.queryByRole('group', { name: 'My account' })).not.toBeInTheDocument();
  });

  it('shows the number of pending run requests beside Runs, from the count route', async () => {
    let listed = false;
    mockApi({
      'GET /api/v1/orgs/acme': ORG_OK,
      'GET /api/v1/auth/me': ME_OK,
      'GET /api/v1/orgs': ORGS_OK,
      // The count, not the length of a list the server caps.
      'GET /api/v1/orgs/acme/run-requests/pending-count': { count: 7 },
      'GET /api/v1/orgs/acme/run-requests': () => {
        listed = true;
        return { requests: [{ id: 'a' }, { id: 'b' }] };
      },
    });
    renderAt('/o/acme/hosts');

    expect(await screen.findByText('7 pending run requests')).toBeInTheDocument();
    expect(listed).toBe(false);
  });

  it('shows the manager version from the setup status in the shell', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': ORG_OK,
      'GET /api/v1/auth/me': ME_OK,
      'GET /api/v1/orgs': ORGS_OK,
      'GET /api/v1/setup/status': { completed: true, version: '0.2.0' },
    });
    renderAt('/o/acme/hosts');

    expect(await screen.findByTitle('Pulsix version')).toHaveTextContent('v0.2.0');
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

  it.each([
    [true, 'ENROLMENT'],
    [false, 'LOGIN PAGE'],
  ])(
    'sends a session that is not MFA-complete (enrolment required: %s) on to %s without requesting the organization',
    async (enrolmentRequired, landing) => {
      let orgRequested = false;
      mockApi({
        'GET /api/v1/auth/me': { ...ME_OK, mfa_complete: false, mfa_enrolment_required: enrolmentRequired },
        'GET /api/v1/orgs/acme': () => {
          orgRequested = true;
          return ORG_OK;
        },
        'GET /api/v1/orgs': ORGS_OK,
      });
      renderAt('/o/acme/hosts');

      expect(await screen.findByText(landing)).toBeInTheDocument();
      expect(orgRequested).toBe(false);
    },
  );

  it('sends a session whose account must change its password to /account/password without requesting the organization', async () => {
    let orgRequested = false;
    mockApi({
      'GET /api/v1/auth/me': { ...ME_OK, mfa_enrolment_required: false, must_change_password: true },
      'GET /api/v1/orgs/acme': () => {
        orgRequested = true;
        return ORG_OK;
      },
      'GET /api/v1/orgs': ORGS_OK,
    });
    renderAt('/o/acme/hosts');

    expect(await screen.findByText('CHANGE PASSWORD')).toBeInTheDocument();
    expect(orgRequested).toBe(false);
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

  it.each([
    [true, ['Acme Hosting', 'New organization']],
    [false, ['Acme Hosting']],
  ])('offers the org switcher a create entry only to an instance admin (admin: %s)', async (admin, labels) => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': ORG_OK,
      'GET /api/v1/auth/me': { ...ME_OK, instance_admin: admin },
      'GET /api/v1/orgs': ORGS_OK,
    });
    renderAt('/o/acme/hosts');

    await user.click(await screen.findByRole('combobox', { name: 'Organization' }));
    expect(screen.getAllByRole('option').map((o) => o.getAttribute('data-label'))).toEqual(labels);
  });

  it('opens the create dialog from the switcher entry', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': ORG_OK,
      'GET /api/v1/auth/me': { ...ME_OK, instance_admin: true },
      'GET /api/v1/orgs': ORGS_OK,
    });
    renderAt('/o/acme/hosts');

    await user.click(await screen.findByRole('combobox', { name: 'Organization' }));
    await user.click(screen.getByRole('option', { name: 'New organization' }));

    expect(await screen.findByRole('dialog', { name: 'New organization' })).toBeInTheDocument();
  });

  it.each([
    [undefined, 'Owner'],
    ['de' as const, 'Inhaber'],
  ])('labels the user\'s role in the active language (%s)', async (locale, label) => {
    mockApi({
      'GET /api/v1/orgs/acme': ORG_OK,
      'GET /api/v1/auth/me': ME_OK,
      'GET /api/v1/orgs': ORGS_OK,
    });
    renderAt('/o/acme/hosts', locale);

    expect(await screen.findByText('HOSTS PAGE')).toBeInTheDocument();
    expect(screen.getByText('Tim R.').parentElement).toHaveTextContent(label);
  });
});
