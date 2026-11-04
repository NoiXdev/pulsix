import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import type { AdminUser } from '../../api/types.ts';
import { UsersPage } from './UsersPage.tsx';

const user = (id: string, name: string, state: AdminUser['state'], over: Partial<AdminUser> = {}): AdminUser => ({
  id,
  email: `${name.toLowerCase()}@acme.io`,
  display_name: name,
  instance_admin: false,
  state,
  org_count: 1,
  last_sign_in_at: null,
  ...over,
});

const USERS = [
  user('u1', 'Ada', 'active', { instance_admin: true, org_count: 3, last_sign_in_at: '2026-11-01T09:00:00Z' }),
  user('u2', 'Ben', 'deactivated'),
  user('u3', 'Cleo', 'invite_pending', { org_count: 0 }),
  user('u4', 'Dan', 'must_change_password'),
  user('u5', 'Eve', 'no_second_factor'),
];

function render() {
  return renderRoutes(
    [
      { path: '/settings/users', component: UsersPage },
      { path: '/settings/users/$userId', component: () => <div>account page</div> },
    ],
    '/o/acme/settings/users',
  );
}

describe('UsersPage', () => {
  it('renders every account with its state pill, admin badge and organization count', async () => {
    mockApi({ 'GET /api/v1/admin/users': { users: USERS, next_cursor: '' } });
    render();

    expect(await screen.findByText('Ada')).toBeInTheDocument();
    for (const label of ['Active', 'Deactivated', 'Invite pending', 'Must change password', 'No second factor']) {
      expect(screen.getByText(label)).toBeInTheDocument();
    }
    expect(screen.getByText('instance admin')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Ada' })).toHaveTextContent('3');
    expect(screen.getByRole('link', { name: 'Ben' })).toHaveTextContent('never');
  });

  it('sends the search term with the query', async () => {
    const u = userEvent.setup();
    const terms: (string | null)[] = [];
    mockApi({
      'GET /api/v1/admin/users': (req: MockRequest) => {
        const q = req.search.get('q');
        terms.push(q);
        return { users: q ? USERS.filter((x) => x.display_name.toLowerCase().includes(q)) : USERS, next_cursor: '' };
      },
    });
    render();

    await screen.findByText('Ada');
    await u.type(screen.getByRole('searchbox', { name: 'Search users' }), 'eve');

    await waitFor(() => expect(screen.queryByText('Ada')).not.toBeInTheDocument());
    expect(screen.getByText('Eve')).toBeInTheDocument();
    expect(terms).toContain('eve');
  });

  it('opens the account page from a row', async () => {
    const u = userEvent.setup();
    mockApi({ 'GET /api/v1/admin/users': { users: USERS, next_cursor: '' } });
    const router = render();

    const row = await screen.findByRole('link', { name: 'Ben' });
    expect(row).toHaveAttribute('href', '/o/acme/settings/users/u2');
    await u.click(row);
    expect(await screen.findByText('account page')).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/o/acme/settings/users/u2');
  });

  it('loads the next page through the cursor', async () => {
    const u = userEvent.setup();
    mockApi({
      'GET /api/v1/admin/users': (req: MockRequest) =>
        req.search.get('cursor') === 'c2'
          ? { users: [user('u9', 'Zoe', 'active')], next_cursor: '' }
          : { users: USERS, next_cursor: 'c2' },
    });
    render();

    await u.click(await screen.findByRole('button', { name: 'Load more' }));
    expect(await screen.findByText('Zoe')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument();
  });

  it('says so when no account matches', async () => {
    mockApi({ 'GET /api/v1/admin/users': { users: [], next_cursor: '' } });
    render();
    expect(await screen.findByText('No account matches.')).toBeInTheDocument();
  });
});
