import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import type { AdminUser } from '../../api/types.ts';
import type { Locale } from '../../i18n/types.ts';
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

const PREFS = { density: 'standard', page_size: 25, lists: {} };

function many(n: number) {
  return Array.from({ length: n }, (_, i) => user(`m${i + 1}`, `Person${String(i + 1).padStart(2, '0')}`, 'active'));
}

/** The answer for one page of `all`, as the server pages it. */
function pageOf(all: AdminUser[], req: MockRequest) {
  const page = Number(req.search.get('page') ?? '1');
  const perPage = Number(req.search.get('per_page') ?? '25');
  return { users: all.slice((page - 1) * perPage, page * perPage), next_cursor: '', total: all.length };
}

let requests: URLSearchParams[] = [];

function setup(users: AdminUser[] | ((req: MockRequest) => unknown), initial = '/o/acme/settings/users', opts: { locale?: Locale; prefs?: object } = {}) {
  requests = [];
  mockApi({
    'GET /api/v1/auth/me/preferences': { ...PREFS, ...opts.prefs },
    'GET /api/v1/admin/users': (req: MockRequest) => {
      requests.push(req.search);
      return typeof users === 'function' ? users(req) : pageOf(users, req);
    },
  });
  return renderListPage({
    path: '/settings/users',
    component: UsersPage,
    initial,
    others: [{ path: '/settings/users/$userId', component: () => <div>account page</div> }],
    locale: opts.locale,
  });
}

describe('UsersPage', () => {
  it('renders every account with its state pill, admin badge and organization count', async () => {
    setup(USERS);

    const ada = (await screen.findByRole('link', { name: 'ada@acme.io' })).closest('[role="row"]') as HTMLElement;
    for (const label of ['Active', 'Deactivated', 'Invite pending', 'Must change password', 'No second factor']) {
      expect(screen.getByText(label)).toBeInTheDocument();
    }
    expect(within(ada).getByText('instance admin')).toBeInTheDocument();
    expect(within(ada).getByText('Ada')).toBeInTheDocument();
    expect(within(ada).getByText('3')).toBeInTheDocument();
    const ben = screen.getByRole('link', { name: 'ben@acme.io' }).closest('[role="row"]') as HTMLElement;
    expect(within(ben).getByText('never')).toBeInTheDocument();
  });

  it('asks for page 1 at the saved size and shows the footer, with no "Load more"', async () => {
    setup(many(30));

    expect(await screen.findByText('1–25 of 30 accounts')).toBeInTheDocument();
    expect(requests).toHaveLength(1);
    expect(requests[0].get('page')).toBe('1');
    expect(requests[0].get('per_page')).toBe('25');
    expect(requests[0].get('sort')).toBeNull();
    expect(requests[0].get('order')).toBeNull();
    expect(requests[0].get('cursor')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument();
  });

  it('sorts by e-mail, name and last sign-in through the URL; the derived state and the count do not sort', async () => {
    const u = userEvent.setup();
    const router = setup(many(30));
    await screen.findByText('1–25 of 30 accounts');

    expect(screen.queryByRole('button', { name: 'State' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Organizations' })).not.toBeInTheDocument();

    await u.click(screen.getByRole('button', { name: /^E-mail/ }));
    await waitFor(() => expect(requests.at(-1)?.get('sort')).toBe('email'));
    expect(requests.at(-1)?.get('order')).toBe('asc');
    expect(router.state.location.search).toMatchObject({ sort: 'email', order: 'asc' });

    await u.click(screen.getByRole('button', { name: /^Name/ }));
    await waitFor(() => expect(requests.at(-1)?.get('sort')).toBe('name'));

    await u.click(screen.getByRole('button', { name: /Last sign-in/ }));
    await waitFor(() => expect(requests.at(-1)?.get('sort')).toBe('last_sign_in'));
  });

  it('reads the sort and page from the URL', async () => {
    setup(many(30), '/o/acme/settings/users?sort=name&order=desc&page=2');
    expect(await screen.findByText('26–30 of 30 accounts')).toBeInTheDocument();
    expect(requests[0].get('sort')).toBe('name');
    expect(requests[0].get('order')).toBe('desc');
    expect(requests[0].get('page')).toBe('2');
  });

  it('shows the last page for a page past the end, not the empty state', async () => {
    setup(many(30), '/o/acme/settings/users?page=9');
    expect(await screen.findByText('26–30 of 30 accounts')).toBeInTheDocument();
    expect(screen.queryByText('No account matches.')).not.toBeInTheDocument();
  });

  it('sends the search term once typing pauses, back at page 1', async () => {
    const u = userEvent.setup();
    const router = setup((req) => {
      const q = req.search.get('q');
      return pageOf(q ? many(30).filter((x) => x.email.includes(q)) : many(30), req);
    }, '/o/acme/settings/users?page=2');

    await screen.findByText('26–30 of 30 accounts');
    await u.type(screen.getByRole('searchbox', { name: 'Search users' }), 'person07');

    await waitFor(() => expect(requests.at(-1)?.get('q')).toBe('person07'));
    // Debounced: the term goes out once typing pauses, not per keystroke.
    expect(requests.map((r) => r.get('q'))).toEqual([null, 'person07']);
    // No request carried the new term on the old page.
    expect(requests.filter((r) => r.get('q') === 'person07').every((r) => r.get('page') === '1')).toBe(true);
    expect(await screen.findByRole('link', { name: 'person07@acme.io' })).toBeInTheDocument();
    expect(router.state.location.search).not.toHaveProperty('page');
  });

  it('opens the account page from a row', async () => {
    const u = userEvent.setup();
    const router = setup(USERS);

    const row = await screen.findByRole('link', { name: 'ben@acme.io' });
    expect(row).toHaveAttribute('href', '/o/acme/settings/users/u2');
    await u.click(row);
    expect(await screen.findByText('account page')).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/o/acme/settings/users/u2');
  });

  it('says so when no account matches', async () => {
    setup([]);
    expect(await screen.findByText('No account matches.')).toBeInTheDocument();
  });

  it('offers every column, shown by default', async () => {
    setup(USERS);
    await screen.findByRole('link', { name: 'ada@acme.io' });
    for (const name of ['E-mail', 'Name', 'State', 'Organizations', 'Last sign-in']) {
      expect(screen.getByRole('columnheader', { name: new RegExp(name) })).toBeInTheDocument();
    }
  });

  it('hides a column the person turned off', async () => {
    setup(USERS, '/o/acme/settings/users', { prefs: { lists: { users: { columns: ['email', 'name'] } } } });
    await screen.findByRole('link', { name: 'ada@acme.io' });
    expect(screen.queryByRole('columnheader', { name: /Organizations/ })).not.toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: /Name/ })).toBeInTheDocument();
  });

  it('says so in German', async () => {
    setup(many(30), '/o/acme/settings/users', { locale: 'de' });
    expect(await screen.findByText('1–25 von 30 Konten')).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: /E-Mail/ })).toBeInTheDocument();
  });
});
