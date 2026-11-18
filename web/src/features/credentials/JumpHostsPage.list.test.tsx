import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { findConfirmDialog } from '../../test/confirmDialog.ts';
import { firstCells, hideColumn, numbered, openRowMenu, sortBy } from '../../test/wholeList.ts';
import { JumpHostsPage } from './JumpHostsPage.tsx';
import { validateJumpHostsSearch } from './jumpHostsSearch.ts';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const FP = 'SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA';
const bastion = (name: string, extra: Record<string, unknown> = {}) => ({
  id: `id-${name}`, name, address: `${name}.example`, port: 22, username: 'ops', ...extra,
});

function setup(opts: { role?: string; bastions?: unknown[]; locale?: 'de'; extra?: Record<string, unknown>; initial?: string } = {}) {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme': { org: { id: 'o', name: 'Acme', slug: 'acme', role: opts.role ?? 'admin', created_at: '' } },
    'GET /api/v1/orgs/acme/bastions': { bastions: opts.bastions ?? numbered('jump', 30).map((n) => bastion(n)) },
    'GET /api/v1/orgs/acme/credentials': { credentials: [] },
    'GET /api/v1/orgs/acme/zones': { zones: [] },
    ...opts.extra,
  });
  return renderListPage({ path: '/jump-hosts', component: JumpHostsPage, initial: opts.initial ?? '/o/acme/jump-hosts', locale: opts.locale, extraSearch: (s) => ({ ...validateJumpHostsSearch(s) }) });
}

describe('JumpHostsPage list', () => {
  it('sorts by name in the browser, jump9 before jump10', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 jump hosts');
    await sortBy(user, 'Name');
    await waitFor(() => expect(firstCells().slice(0, 10)).toEqual(['jump1', 'jump2', 'jump3', 'jump4', 'jump5', 'jump6', 'jump7', 'jump8', 'jump9', 'jump10']));
    expect(router.state.location.search).toMatchObject({ sort: 'name', order: 'asc' });
  });

  it('keeps no inline buttons; the row menu opens the same delete confirmation', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 jump hosts');
    expect(screen.queryByRole('button', { name: /^Delete/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Edit/ })).not.toBeInTheDocument();
    await openRowMenu(user, 'jump30');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Edit', 'Delete']);
    await user.click(screen.getByRole('menuitem', { name: 'Delete' }));
    expect(await findConfirmDialog()).toHaveTextContent('jump30');
  });

  it('edits from the row menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 jump hosts');
    await openRowMenu(user, 'jump30');
    await user.click(screen.getByRole('menuitem', { name: 'Edit' }));
    expect(await screen.findByRole('dialog', { name: /jump host/i })).toBeInTheDocument();
  });

  it('offers Pin for an unpinned host key and posts the observed fingerprint', async () => {
    const user = userEvent.setup();
    let pinned: unknown;
    setup({
      bastions: [bastion('jump-a', { observed_key: FP, observed_state: 'unpinned' })],
      extra: { 'PUT /api/v1/orgs/acme/bastions/id-jump-a/host-key': (req: MockRequest) => { pinned = req.body; return {}; } },
    });
    await openRowMenu(user, 'jump-a');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Edit', 'Delete', 'Pin host key']);
    await user.click(screen.getByRole('menuitem', { name: 'Pin host key' }));
    await waitFor(() => expect(pinned).toEqual({ fingerprint: FP }));
    expect(await screen.findByText('Jump host key of jump-a pinned')).toBeInTheDocument();
  });

  it('offers Unpin for a pinned key, and neither for a changed key', async () => {
    const user = userEvent.setup();
    setup({
      bastions: [
        bastion('jump-p', { ssh_host_key: FP, pin_state: 'full', observed_key: FP, observed_state: 'pinned' }),
        bastion('jump-c', { ssh_host_key: FP, pin_state: 'full', observed_key: 'SHA256:other', observed_state: 'changed' }),
      ],
    });
    await openRowMenu(user, 'jump-p');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Edit', 'Delete', 'Unpin host key']);
    await user.keyboard('{Escape}');
    await openRowMenu(user, 'jump-c');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Edit', 'Delete', 'Pin host key']);
  });

  it('gives a viewer no menu at all', async () => {
    setup({ role: 'viewer', bastions: [bastion('jump-a', { observed_key: FP, observed_state: 'unpinned' })] });
    expect(await screen.findByText(FP)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Actions for/ })).not.toBeInTheDocument();
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 jump hosts');
    await hideColumn(user, 'Login');
    expect(screen.queryByRole('columnheader', { name: 'Login' })).not.toBeInTheDocument();
  });

  it('pages 30 rows and follows the page size', async () => {
    const user = userEvent.setup();
    setup();
    expect(await screen.findByText('1–25 of 30 jump hosts')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 jump hosts')).toBeInTheDocument();
    await pickOption(screen, 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 jump hosts')).toBeInTheDocument();
  });

  it('writes the footer and the menu in German', async () => {
    const user = userEvent.setup();
    setup({ locale: 'de' });
    expect(await screen.findByText(/^1–25 von 30 Sprunghosts/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Aktionen für jump30' }));
    expect(screen.getAllByRole('menuitem')).toHaveLength(2);
  });

  describe('filters', () => {
    const bastions = [
      bastion('edge', { credential_id: 'c1', ssh_host_key: FP }),
      bastion('core', { credential_id: 'c2', observed_state: 'changed' }),
      bastion('lab', { credential_id: 'c1' }),
    ];
    const extra = { 'GET /api/v1/orgs/acme/credentials': { credentials: [{ id: 'c1', name: 'ops-key' }, { id: 'c2', name: 'other-key' }] } };

    it('reads the host key and credential filters from the URL', async () => {
      setup({ bastions, extra, initial: '/o/acme/jump-hosts?key=changed' });
      await screen.findByText('1–1 of 1 jump host');
      expect(firstCells()).toEqual(['core']);
      expect(screen.getByText('1 of 3 jump hosts')).toBeInTheDocument();
    });

    it('keeps a credential only the URL names in the picker, so it can be cleared', async () => {
      setup({ bastions, extra, initial: '/o/acme/jump-hosts?credential=gone-credential' });
      expect(await screen.findByLabelText('Filter by credential')).toHaveValue('gone-cre');
    });

    it('filters by credential and writes the search to the URL', async () => {
      const user = userEvent.setup();
      const router = setup({ bastions, extra, initial: '/o/acme/jump-hosts?credential=c1' });
      await screen.findByText('1–2 of 2 jump hosts');
      expect(firstCells().sort()).toEqual(['edge', 'lab']);
      await user.type(screen.getByRole('searchbox', { name: 'Search jump hosts' }), 'la');
      await waitFor(() => expect(router.state.location.search).toEqual({ credential: 'c1', q: 'la' }));
      expect(firstCells()).toEqual(['lab']);
      await user.click(screen.getByRole('button', { name: 'Reset every filter' }));
      await waitFor(() => expect(router.state.location.search).toEqual({}));
    });
  });
});
