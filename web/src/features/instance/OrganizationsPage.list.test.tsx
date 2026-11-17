import { describe, expect, it } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { firstCells, hideColumn, numbered, sortBy } from '../../test/wholeList.ts';
import { OrganizationsPage } from './OrganizationsPage.tsx';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const org = (slug: string, over: Record<string, unknown> = {}) => ({
  id: `id-${slug}`, slug, name: slug, member_count: 1, created_at: '2026-03-02T09:00:00Z', actor_is_member: true, ...over,
});

function setup(opts: { locale?: 'de'; orgs?: unknown[] } = {}) {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/admin/orgs': { orgs: opts.orgs ?? numbered('o', 30).map((n) => org(n)) },
  });
  return renderListPage({
    path: '/settings/organizations',
    component: OrganizationsPage,
    initial: '/o/acme/settings/organizations',
    locale: opts.locale,
    others: [{ path: '/hosts', component: () => <div>hosts page</div> }],
  });
}

describe('OrganizationsPage list', () => {
  it('sorts by name in the browser, o9 before o10', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 organizations');
    await sortBy(user, 'Organization');
    await waitFor(() => expect(firstCells().slice(0, 10)).toEqual(['o1', 'o2', 'o3', 'o4', 'o5', 'o6', 'o7', 'o8', 'o9', 'o10']));
    expect(router.state.location.search).toMatchObject({ sort: 'name', order: 'asc' });
  });

  it('sorts the member count as a number', async () => {
    const user = userEvent.setup();
    setup({ orgs: [org('a', { member_count: 10 }), org('b', { member_count: 2 }), org('c', { member_count: 9 })] });
    await screen.findByText('1–3 of 3 organizations');
    await sortBy(user, 'Members');
    await waitFor(() => expect(firstCells()).toEqual(['b', 'c', 'a']));
  });

  it('has no inline buttons and no row menu', async () => {
    setup();
    await screen.findByText('1–25 of 30 organizations');
    expect(screen.queryAllByRole('button', { name: /^Actions for/ })).toEqual([]);
  });

  it('links a member row to its hosts page and keeps the slug in its own column', async () => {
    setup({ orgs: [org('acme', { name: 'Acme Hosting' })] });
    const link = await screen.findByRole('link', { name: 'Acme Hosting' });
    expect(link).toHaveAttribute('href', '/o/acme/hosts');
    expect(screen.getByRole('columnheader', { name: 'Slug' })).toBeInTheDocument();
    expect(screen.getByText('acme')).toBeInTheDocument();
  });

  it('gives a row the admin is not a member of its reason as text, not only a title', async () => {
    setup({ orgs: [org('globex', { name: 'Globex', actor_is_member: false })] });
    const cell = (await screen.findByText('Globex')).closest('[role="cell"]') as HTMLElement;
    expect(within(cell).getByText('not a member')).toBeInTheDocument();
    expect(within(cell).getByText('You are not a member of this organization, so you cannot open it.')).toHaveClass('sr-only');
    expect(screen.queryByRole('link', { name: 'Globex' })).not.toBeInTheDocument();
    // No menu either: the browser's own context menu stays.
    expect(fireEvent.contextMenu(cell)).toBe(true);
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 organizations');
    await hideColumn(user, 'Created');
    expect(screen.queryByRole('columnheader', { name: 'Created' })).not.toBeInTheDocument();
  });

  it('pages 30 rows and follows the page size', async () => {
    const user = userEvent.setup();
    setup();
    expect(await screen.findByText('1–25 of 30 organizations')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 organizations')).toBeInTheDocument();
    await pickOption(screen, 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 organizations')).toBeInTheDocument();
  });

  it('writes the footer in German', async () => {
    setup({ locale: 'de' });
    expect(await screen.findByText('1–25 von 30 Organisationen')).toBeInTheDocument();
  });
});
