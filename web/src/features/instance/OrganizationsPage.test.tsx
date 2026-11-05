import { describe, expect, it } from 'vitest';
import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi } from '../../test/mockApi.ts';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import type { AdminOrg } from '../../api/types.ts';
import { OrganizationsPage } from './OrganizationsPage.tsx';

const org = (slug: string, name: string, over: Partial<AdminOrg> = {}): AdminOrg => ({
  id: `id-${slug}`,
  slug,
  name,
  member_count: 1,
  created_at: '2026-03-02T09:00:00Z',
  actor_is_member: true,
  ...over,
});

const ORGS = [
  org('acme', 'Acme Hosting', { member_count: 12 }),
  org('globex', 'Globex', { member_count: 3, actor_is_member: false }),
];

function render() {
  return renderRoutes(
    [
      { path: '/settings/organizations', component: OrganizationsPage },
      { path: '/hosts', component: () => <div>hosts page</div> },
    ],
    '/o/acme/settings/organizations',
  );
}

describe('OrganizationsPage', () => {
  // Verified for backlog 7.82 item 18: the guard returns null while the
  // query is pending, so the page's own loading branch is the one shown.
  it('shows the loading state while the list is on its way', async () => {
    mockApi({ 'GET /api/v1/admin/orgs': () => new Promise(() => {}) });
    render();

    expect(await screen.findByText('Loading…')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Organizations' })).toBeInTheDocument();
  });

  it('lists every organization with its slug, member count and creation date', async () => {
    mockApi({ 'GET /api/v1/admin/orgs': { orgs: ORGS } });
    render();

    expect(await screen.findByRole('link', { name: 'Acme Hosting' })).toBeInTheDocument();
    expect(screen.getByText('acme')).toBeInTheDocument();
    expect(screen.getByText('Globex')).toBeInTheDocument();
    expect(screen.getByText('globex')).toBeInTheDocument();
    expect(screen.getByText('12')).toBeInTheDocument();
    expect(screen.getByText('3')).toBeInTheDocument();
    expect(screen.getAllByText(new Date('2026-03-02T09:00:00Z').toLocaleDateString('en'))).toHaveLength(2);
  });

  it('no longer says the count includes deactivated accounts, which it does not', async () => {
    mockApi({ 'GET /api/v1/admin/orgs': { orgs: ORGS } });
    render();

    expect(await screen.findByText('Every organization on this instance.')).toBeInTheDocument();
    expect(screen.queryByText(/deactivated/i)).not.toBeInTheDocument();
  });

  it('links an organization the admin is a member of to its hosts page', async () => {
    const user = userEvent.setup();
    mockApi({ 'GET /api/v1/admin/orgs': { orgs: ORGS } });
    const router = render();

    const link = await screen.findByRole('link', { name: 'Acme Hosting' });
    expect(link).toHaveAttribute('href', '/o/acme/hosts');
    await user.click(link);
    expect(await screen.findByText('hosts page')).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/o/acme/hosts');
  });

  it('shows another organization as plain text', async () => {
    mockApi({ 'GET /api/v1/admin/orgs': { orgs: ORGS } });
    render();

    await screen.findByText('Globex');
    expect(screen.queryByRole('link', { name: 'Globex' })).not.toBeInTheDocument();
    expect(screen.getAllByRole('link')).toHaveLength(1);
    expect(screen.getByText('not a member')).toBeInTheDocument();
  });

  it('keeps the non-member row\'s title and its row layout', async () => {
    mockApi({ 'GET /api/v1/admin/orgs': { orgs: ORGS } });
    render();

    const row = (await screen.findByText('Globex')).closest('[title]') as HTMLElement;
    expect(row).toHaveAttribute('title', 'You are not a member of this organization, so you cannot open it.');
    expect(row).toHaveStyle({ gridTemplateColumns: 'minmax(0, 1.6fr) 120px 140px' });
    expect(within(row).queryByRole('link')).not.toBeInTheDocument();
  });

  it('opens the create dialog from the page', async () => {
    const user = userEvent.setup();
    mockApi({ 'GET /api/v1/admin/orgs': { orgs: ORGS } });
    render();

    await screen.findByRole('link', { name: 'Acme Hosting' });
    await user.click(screen.getByRole('button', { name: 'New organization' }));

    expect(await screen.findByRole('dialog', { name: 'New organization' })).toBeInTheDocument();
  });
});
