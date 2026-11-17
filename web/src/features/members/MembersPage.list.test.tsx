import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { acceptConfirm } from '../../test/confirmDialog.ts';
import { chooseRowAction, hideColumn, numbered, openRowMenu } from '../../test/wholeList.ts';
import { MembersPage } from './MembersPage.tsx';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });
const member = (name: string, over: Record<string, unknown> = {}) => ({
  user_id: `u-${name}`, display_name: name, email: `${name}@acme.io`, role: 'viewer', created_at: '2026-01-01T00:00:00Z', disabled_at: null, ...over,
});
/** The member names in order: the first cell without its initials avatar. */
function names(root: ParentNode = document): string[] {
  return Array.from(root.querySelectorAll('[data-list-row]')).map((row) => {
    const cell = row.querySelector('[role="cell"]')!.cloneNode(true) as HTMLElement;
    cell.querySelectorAll('[data-avatar]').forEach((a) => a.remove());
    return cell.textContent ?? '';
  });
}
const invite = (email: string, over: Record<string, unknown> = {}) => ({
  id: `i-${email.split('@')[0]}`, email, role: 'viewer', invited_by: 'u1', invited_by_name: 'Tim', expires_at: '2026-01-08T00:00:00Z', created_at: '2026-01-01T00:00:00Z', ...over,
});

function setup(opts: { locale?: 'de'; role?: string; members?: unknown[]; invites?: unknown[]; onRemove?: () => void; onRevoke?: () => void } = {}) {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme': org(opts.role ?? 'admin'),
    'GET /api/v1/orgs/acme/members': { members: opts.members ?? numbered('m', 30).map((n) => member(n)) },
    'GET /api/v1/orgs/acme/invites': { invites: opts.invites ?? [] },
    'DELETE /api/v1/orgs/acme/members/u-m30': () => {
      opts.onRemove?.();
      return {};
    },
    'DELETE /api/v1/orgs/acme/invites/i-b1': () => {
      opts.onRevoke?.();
      return {};
    },
  });
  return renderListPage({
    path: '/settings/members', component: MembersPage, initial: '/o/acme/settings/members', locale: opts.locale,
    others: [{ path: '/settings/members/$userId', component: () => <div>MEMBER PAGE</div> }],
  });
}

describe('MembersPage list', () => {
  it('sorts by member in the browser, m9 before m10, with the sort in the URL', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 members');
    await user.click(screen.getByRole('button', { name: 'Member' }));
    await waitFor(() => expect(names().slice(0, 10)).toEqual(['m1', 'm2', 'm3', 'm4', 'm5', 'm6', 'm7', 'm8', 'm9', 'm10']));
    expect(router.state.location.search).toMatchObject({ sort: 'name', order: 'asc' });
  });

  it('sorts the role by rank', async () => {
    const user = userEvent.setup();
    setup({ members: [member('a', { role: 'owner' }), member('b', { role: 'admin' }), member('c', { role: 'viewer' })] });
    await screen.findByText('1–3 of 3 members');
    await user.click(screen.getByRole('button', { name: 'Role' }));
    await waitFor(() => expect(names()).toEqual(['c', 'b', 'a']));
  });

  it('links the member to the member page', async () => {
    setup({ members: [member('ada')] });
    const link = await screen.findByRole('link', { name: 'ada' });
    expect(link).toHaveAttribute('href', '/o/acme/settings/members/u-ada');
  });

  it('gives a manager Open and Remove, and Remove asks the same confirmation', async () => {
    const user = userEvent.setup();
    let removed = 0;
    setup({ onRemove: () => removed++ });
    await screen.findByText('1–25 of 30 members');
    await openRowMenu(user, 'm30 (m30@acme.io)');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Open', 'Remove…']);
    await user.click(screen.getByRole('menuitem', { name: 'Remove…' }));
    expect(await acceptConfirm(user)).toContain('m30');
    await waitFor(() => expect(removed).toBe(1));
    expect(await screen.findByText('m30 removed')).toBeInTheDocument();
  });

  it('gives a role without member management Open only', async () => {
    const user = userEvent.setup();
    setup({ role: 'operator' });
    await screen.findByText('1–25 of 30 members');
    await openRowMenu(user, 'm30 (m30@acme.io)');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Open']);
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 members');
    await hideColumn(user, 'Joined');
    expect(screen.queryByRole('columnheader', { name: 'Joined' })).not.toBeInTheDocument();
  });

  it('pages 30 rows and follows the page size', async () => {
    const user = userEvent.setup();
    setup();
    expect(await screen.findByText('1–25 of 30 members')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 members')).toBeInTheDocument();
    await pickOption(screen, 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 members')).toBeInTheDocument();
  });

  it('writes the footer in German', async () => {
    setup({ locale: 'de' });
    expect(await screen.findByText('1–25 von 30 Mitglieder')).toBeInTheDocument();
  });
});

describe('PendingInvites list', () => {
  const invites = () => [invite('b2@acme.io', { role: 'admin' }), invite('b1@acme.io', { role: 'operator' }), invite('b3@acme.io')];

  it('is a second list whose sort and page stay out of the URL', async () => {
    const user = userEvent.setup();
    const router = setup({ members: [member('a'), member('b')], invites: invites() });
    const section = await screen.findByRole('region', { name: 'Open invitations' });
    await within(section).findByText('1–3 of 3 invitations');
    await user.click(within(section).getByRole('button', { name: 'E-mail address' }));
    await waitFor(() =>
      expect(names(section)).toEqual(['b1@acme.io', 'b2@acme.io', 'b3@acme.io']),
    );
    expect(router.state.location.search).toEqual({});
  });

  it('revokes from the row menu with the same confirmation, no inline button', async () => {
    const user = userEvent.setup();
    let revoked = 0;
    setup({ members: [member('a')], invites: invites(), onRevoke: () => revoked++ });
    await screen.findByRole('region', { name: 'Open invitations' });
    expect(screen.queryByRole('button', { name: /^Revoke invitation/ })).not.toBeInTheDocument();
    await chooseRowAction(user, 'b1@acme.io (operator)', 'Revoke…');
    expect(await acceptConfirm(user)).toBe('Revoke the invitation to b1@acme.io? Its link stops working.');
    await waitFor(() => expect(revoked).toBe(1));
  });

  it('shows no section without invitations', async () => {
    setup({ members: [member('a')] });
    await screen.findByText('1–1 of 1 member');
    expect(screen.queryByRole('region', { name: 'Open invitations' })).not.toBeInTheDocument();
  });
});
