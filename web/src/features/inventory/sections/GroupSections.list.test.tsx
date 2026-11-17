import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../../test/mockApi.ts';
import { renderListPage } from '../../../test/renderListPage.tsx';
import { pickOption } from '../../../test/pickOption.ts';
import { findConfirmDialog } from '../../../test/confirmDialog.ts';
import { firstCells, hideColumn, numbered, openRowMenu, sortBy } from '../../../test/wholeList.ts';
import type { InventoryGroup, InventoryGroupSummary } from '../../../api/types.ts';
import { ChildrenSection } from './ChildrenSection.tsx';
import { MembersSection } from './MembersSection.tsx';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const GROUP = { id: 'g1', name: 'web', description: '', parent_id: null, revision: 1, schema: { fields: [] }, vars: {}, problems: [], orphaned: [], created_at: '', updated_at: '' } as unknown as InventoryGroup;
const member = (name: string, extra = {}) => ({ group_id: 'g1', group_name: 'web', host_id: `h-${name}`, host_name: name, vars: {}, complete: true, problems: [], orphaned: [], revision: 1, ...extra });
const child = (name: string): InventoryGroupSummary => ({ id: `c-${name}`, name, description: '', parent_id: 'g1', members: 2, group_fields: 0, host_fields: 0, complete: true, revision: 1 });

function members(canWrite: boolean, locale?: 'de') {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme/inventory/groups/g1/members': { members: numbered('host', 30).map((n) => member(n)) },
  });
  return renderListPage({
    path: '/groups/$id', component: () => <MembersSection org="acme" group={GROUP} canWrite={canWrite} />, initial: '/o/acme/groups/g1', locale,
    others: [{ path: '/hosts/$id', component: () => <div>HOST VIEW</div> }],
  });
}

function children(locale?: 'de') {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
  });
  return renderListPage({
    path: '/groups/$id', component: () => <ChildrenSection org="acme" group={GROUP} groups={numbered('kid', 30).map(child)} canWrite />, initial: '/o/acme/groups/g1', locale,
  });
}

describe('MembersSection list', () => {
  it('sorts by host in the browser without touching the URL', async () => {
    const user = userEvent.setup();
    const router = members(true);
    await screen.findByText('1–25 of 30 hosts');
    await sortBy(user, 'Host');
    await waitFor(() => expect(firstCells().slice(0, 10)).toEqual(['host1', 'host2', 'host3', 'host4', 'host5', 'host6', 'host7', 'host8', 'host9', 'host10']));
    expect(router.state.location.search).toEqual({});
  });

  it('pages in component state: the URL stays clean', async () => {
    const user = userEvent.setup();
    const router = members(true);
    await screen.findByText('1–25 of 30 hosts');
    await user.click(screen.getByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 hosts')).toBeInTheDocument();
    await pickOption(screen, 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 hosts')).toBeInTheDocument();
    expect(router.state.location.search).toEqual({});
  });

  it('keeps no inline buttons; a writer gets Open host, Edit values and Remove, which asks first', async () => {
    const user = userEvent.setup();
    members(true);
    await screen.findByText('1–25 of 30 hosts');
    await openRowMenu(user, 'host30');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Open host', 'Edit values', 'Remove from group…']);
    await user.click(screen.getByRole('menuitem', { name: 'Remove from group…' }));
    expect(await findConfirmDialog()).toHaveTextContent('Remove host30 from web?');
  });

  it('gives a reader only Open host', async () => {
    const user = userEvent.setup();
    members(false);
    await screen.findByText('1–25 of 30 hosts');
    await openRowMenu(user, 'host30');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Open host']);
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    members(true);
    await screen.findByText('1–25 of 30 hosts');
    await hideColumn(user, 'Values');
    expect(screen.queryByRole('columnheader', { name: 'Values' })).not.toBeInTheDocument();
  });

  it('writes the footer in German', async () => {
    members(true, 'de');
    expect(await screen.findByText('1–25 von 30 Hosts')).toBeInTheDocument();
  });
});

describe('ChildrenSection list', () => {
  it('sorts by name in the browser without touching the URL', async () => {
    const user = userEvent.setup();
    const router = children();
    await screen.findByText('1–25 of 30 groups');
    await sortBy(user, 'Group');
    await waitFor(() => expect(firstCells().slice(0, 10)).toEqual(['kid1', 'kid2', 'kid3', 'kid4', 'kid5', 'kid6', 'kid7', 'kid8', 'kid9', 'kid10']));
    expect(router.state.location.search).toEqual({});
  });

  it('pages in component state and links each row', async () => {
    const user = userEvent.setup();
    const router = children();
    await screen.findByText('1–25 of 30 groups');
    expect(within(screen.getByRole('table')).getByRole('link', { name: 'kid30' })).toHaveAttribute('href', '/o/acme/groups/c-kid30');
    await user.click(screen.getByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 groups')).toBeInTheDocument();
    expect(router.state.location.search).toEqual({});
  });

  it('writes the footer in German', async () => {
    children('de');
    expect(await screen.findByText('1–25 von 30 Gruppen')).toBeInTheDocument();
  });
});
