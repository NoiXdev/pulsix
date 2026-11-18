import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { clippedElements } from '../../test/clipped.ts';
import { group, GROUPS, routes, renderAt, openFields, closeFields, summary } from './groupPageTestKit.tsx';
import { acceptConfirm } from '../../test/confirmDialog.ts';

afterEach(() => {
  vi.restoreAllMocks();
  window.history.replaceState(null, '', '/');
});

const fact = (label: string) => within(screen.getByTestId('group-facts')).getByText(label, { selector: 'dt' }).nextElementSibling as HTMLElement;

/** The tab strip, as label → count ('' for a tab without one). */
function tabItems(): Array<[string, string]> {
  return within(screen.getByRole('tablist', { name: 'Group sections' }))
    .getAllByRole('tab')
    .map((tab) => {
      const spans = tab.querySelectorAll('span');
      return [spans[0]?.textContent ?? '', spans[1]?.textContent ?? ''];
    });
}

const tab = (name: string) => screen.getByRole('tab', { name: new RegExp(`^${name}`) });
const selectedTab = () => screen.getAllByRole('tab').find((t) => t.getAttribute('aria-selected') === 'true')?.querySelector('span')?.textContent;
const panel = () => within(screen.getByRole('tabpanel'));

describe('GroupDetailPage header', () => {
  it('shows who the group is: name, completeness, description and the facts', async () => {
    mockApi(routes());
    renderAt('/o/acme/groups/g1');
    const heading = await screen.findByRole('heading', { level: 1, name: 'web' });
    expect(heading).toHaveClass('font-mono');
    expect(await screen.findByText('Incomplete')).toBeInTheDocument();
    expect(screen.getByText('Front web servers')).toBeInTheDocument();

    expect(fact('Parent')).toHaveTextContent('prod');
    expect(within(fact('Parent')).getByRole('link', { name: 'prod' })).toHaveAttribute('href', '/o/acme/groups/p1');
    await waitFor(() => expect(fact('Hosts')).toHaveTextContent('2, 1 to fill in'));
    expect(fact('Subgroups')).toHaveTextContent('1');
    await waitFor(() => expect(fact('Jump host')).toHaveTextContent('bastion-fra'));
  });

  it('adds the hosts of the subgroups when the preview has them', async () => {
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/preview': {
          inventory: { all: { children: ['prod'] }, _meta: { hostvars: { 'web-01': {}, 'web-02': {}, 'eu-01': {} } } },
          connections: {},
          warnings: [],
        },
      }),
    );
    renderAt('/o/acme/groups/g1');
    await screen.findByTestId('group-facts');
    await waitFor(() => expect(fact('Hosts')).toHaveTextContent('2, 1 to fill in · 3 managed incl. subgroups'));
  });

  it('says which group a jump host is inherited from', async () => {
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': group({ bastion_id: undefined }),
        'GET /api/v1/orgs/acme/inventory/groups': {
          groups: [summary('p1', 'prod', null, { bastion_id: 'b1' }), ...GROUPS.groups.filter((g) => g.id !== 'p1').map((g) => ({ ...g, bastion_id: undefined }))],
        },
      }),
    );
    renderAt('/o/acme/groups/g1');
    await screen.findByTestId('group-facts');
    await waitFor(() => expect(fact('Jump host')).toHaveTextContent('bastion-fra (from prod)'));
  });

  it('tells a jump host still loading from none', async () => {
    let answer: (value: unknown) => void = () => {};
    mockApi(routes('admin', { 'GET /api/v1/orgs/acme/bastions': () => new Promise((resolve) => (answer = resolve)) }));
    const { unmount } = renderAt('/o/acme/groups/g1');
    await screen.findByTestId('group-facts');
    expect(fact('Jump host')).toHaveTextContent('Loading…');
    answer({ bastions: [{ id: 'b1', name: 'bastion-fra', address: 'b.example', port: 22, username: 'ops' }] });
    await waitFor(() => expect(fact('Jump host')).toHaveTextContent('bastion-fra'));
    unmount();

    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': group({ bastion_id: undefined }),
        'GET /api/v1/orgs/acme/inventory/groups': { groups: GROUPS.groups.map((g) => ({ ...g, bastion_id: undefined })) },
      }),
    );
    renderAt('/o/acme/groups/g1');
    await screen.findByTestId('group-facts');
    await waitFor(() => expect(fact('Subgroups')).toHaveTextContent('1'));
    expect(fact('Jump host')).toHaveTextContent('—');
  });

  it('offers a writer every action, and the menu Edit … and Delete …', async () => {
    const user = userEvent.setup();
    mockApi(routes());
    renderAt('/o/acme/groups/g1');
    await waitFor(() => expect(screen.getByRole('button', { name: 'Assign host' })).toBeEnabled());
    expect(screen.getByRole('button', { name: 'Subgroup …' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Manage fields …' })).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'More actions for web' }));
    expect(screen.getAllByRole('menuitem').map((m) => m.textContent)).toEqual(['Edit …', 'Delete …']);
  });

  it('gives a viewer only a look at the fields', async () => {
    const user = userEvent.setup();
    mockApi(routes('viewer'));
    renderAt('/o/acme/groups/g1');
    await screen.findByRole('heading', { level: 1, name: 'web' });
    expect(await screen.findByLabelText('env')).toBeDisabled();
    for (const name of ['Assign host', 'Subgroup …', 'Manage fields …', 'More actions for web', 'Save values', 'Discard']) {
      expect(screen.queryByRole('button', { name })).not.toBeInTheDocument();
    }

    // The fields are read-only, under the title "Fields": no field can be
    // added, saved or moved, no badge toggles, but a field still shows its
    // settings, disabled.
    const fields = await openFields(user);
    expect(fields).toHaveAccessibleName('Fields');
    expect(within(fields).queryByRole('button', { name: 'Add field' })).not.toBeInTheDocument();
    expect(within(fields).queryByRole('button', { name: 'Save' })).not.toBeInTheDocument();
    expect(within(fields).queryByRole('button', { name: /^(Type|Scope|Requirement) of env/ })).not.toBeInTheDocument();
    expect(within(fields).getByRole('img', { name: 'Scope of env: Group' })).toBeInTheDocument();
    await user.click(within(fields).getByRole('button', { name: /^domain\b/ }));
    const panel = within(fields).getByRole('tabpanel');
    expect(within(panel).getByLabelText('Name')).toHaveValue('domain');
    expect(within(panel).getByLabelText('Name')).toBeDisabled();
    expect(within(panel).getByRole('switch', { name: 'Active' })).toBeDisabled();
    await closeFields(user);

    // The members' menu only opens the host.
    await user.click(tab('Members'));
    await user.click(await screen.findByRole('button', { name: 'Actions for web-01' }));
    expect(screen.getAllByRole('menuitem').map((m) => m.textContent)).toEqual(['Open host']);
  });

  it('assigns a host from the header', async () => {
    const user = userEvent.setup();
    mockApi(routes('admin', { 'GET /api/v1/orgs/acme/hosts': { hosts: [], next_cursor: '' } }));
    renderAt('/o/acme/groups/g1');
    await waitFor(() => expect(screen.getByRole('button', { name: 'Assign host' })).toBeEnabled());
    await user.click(screen.getByRole('button', { name: 'Assign host' }));
    expect(await screen.findByRole('dialog', { name: 'Assign host to web' })).toBeInTheDocument();
  });

  it('adds a subgroup from the header and opens its fields', async () => {
    const user = userEvent.setup();
    let posted: MockRequest | undefined;
    const created = group({ id: 'n1', name: 'web_us', parent_id: 'g1', schema: { fields: [] }, vars: {} });
    mockApi(
      routes('admin', {
        'POST /api/v1/orgs/acme/inventory/groups': (req: MockRequest) => {
          posted = req;
          return created;
        },
        'GET /api/v1/orgs/acme/inventory/groups/n1': created,
        'GET /api/v1/orgs/acme/inventory/groups/n1/members': { members: [] },
      }),
    );
    const { router } = renderAt('/o/acme/groups/g1');
    await user.click(await screen.findByRole('button', { name: 'Subgroup …' }));
    const dialog = await screen.findByRole('dialog', { name: 'Add child group' });
    await user.type(within(dialog).getByLabelText('Name'), 'web_us');
    await user.click(within(dialog).getByRole('button', { name: 'Create group' }));
    await waitFor(() => expect(router.state.location.pathname).toBe('/o/acme/groups/n1'));
    expect(posted?.body).toMatchObject({ name: 'web_us', parent_id: 'g1' });
    expect(await screen.findByRole('dialog', { name: 'Manage fields' })).toBeInTheDocument();
    expect(router.state.location.hash).toBe('schema');

    // Closed, the fields do not open again on a reload.
    await closeFields(user);
    await waitFor(() => expect(router.state.location.hash).toBe(''));
  });

  it('edits name, parent and jump host in a dialog and deletes from the menu', async () => {
    const user = userEvent.setup();
    let patched: MockRequest | undefined;
    let deleted = false;
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': group({ bastion_id: undefined }),
        'PATCH /api/v1/orgs/acme/inventory/groups/g1': (req: MockRequest) => {
          patched = req;
          return group();
        },
        'DELETE /api/v1/orgs/acme/inventory/groups/g1': () => {
          deleted = true;
          return { ok: true };
        },
      }),
    );
    const { router } = renderAt('/o/acme/groups/g1');
    await user.click(await screen.findByRole('button', { name: 'More actions for web' }));
    await user.click(screen.getByRole('menuitem', { name: 'Edit …' }));
    const dialog = await screen.findByRole('dialog', { name: 'Edit group' });
    expect(within(dialog).queryByRole('button', { name: /Delete/ })).not.toBeInTheDocument();
    await user.clear(within(dialog).getByLabelText('Name'));
    await user.type(within(dialog).getByLabelText('Name'), 'web_fe');
    await pickOption(within(dialog), 'Parent', { value: 'p2' });
    await within(dialog).findByLabelText('Jump host');
    await pickOption(within(dialog), 'Jump host', { value: 'b1' });
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    await screen.findByText('Group saved');
    expect(patched?.headers.get('If-Match')).toBe('"3"');
    expect(patched?.body).toEqual({ name: 'web_fe', description: 'Front web servers', parent_id: 'p2', bastion_id: 'b1' });
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Edit group' })).not.toBeInTheDocument());

    await user.click(screen.getByRole('button', { name: 'More actions for web' }));
    await user.click(screen.getByRole('menuitem', { name: 'Delete …' }));
    expect(await acceptConfirm(user)).toBe('Delete group web? Its members lose the values they have for this group.');
    expect(await screen.findByText('GROUPS')).toBeInTheDocument();
    expect(deleted).toBe(true);
    expect(router.state.location.pathname).toBe('/o/acme/groups');
  });

  it('reports a refused delete and stays on the group', async () => {
    const user = userEvent.setup();
    mockApi(
      routes('admin', {
        'DELETE /api/v1/orgs/acme/inventory/groups/g1': () =>
          new Response(JSON.stringify({ status: 409, detail: 'the group still has child groups; move or delete them first' }), {
            status: 409, headers: { 'Content-Type': 'application/json' },
          }),
      }),
    );
    const { router } = renderAt('/o/acme/groups/g1');
    await user.click(await screen.findByRole('button', { name: 'More actions for web' }));
    await user.click(screen.getByRole('menuitem', { name: 'Delete …' }));
    await acceptConfirm(user);
    expect(await screen.findByText(/still has child groups/)).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/o/acme/groups/g1');
  });
});

describe('GroupDetailPage tabs', () => {
  it('shows the tabs with their counts, on Variables by default', async () => {
    mockApi(routes());
    renderAt('/o/acme/groups/g1');
    await screen.findByRole('tablist', { name: 'Group sections' });
    await waitFor(() =>
      expect(tabItems()).toEqual([
        ['Variables', ''],
        ['Members', '2'],
        ['Subgroups', '1'],
        ['Inheritance', ''],
        ['Inventory', ''],
      ]),
    );
    expect(selectedTab()).toBe('Variables');
    expect(screen.getByRole('tabpanel')).toHaveAttribute('aria-labelledby', tab('Variables').id);
    expect(await panel().findByRole('region', { name: 'Group values' })).toBeInTheDocument();
  });

  it('switches tabs and keeps the open one in the URL', async () => {
    const user = userEvent.setup();
    mockApi(routes());
    const { router } = renderAt('/o/acme/groups/g1');
    await user.click(await screen.findByRole('tab', { name: /^Subgroups/ }));
    expect(router.state.location.search).toEqual({ tab: 'subgroups' });
    expect(selectedTab()).toBe('Subgroups');
    await user.click(tab('Variables'));
    expect(router.state.location.search).toEqual({});
  });

  it('moves between the tabs with the arrow keys, replacing the history entry', async () => {
    const user = userEvent.setup();
    mockApi(routes());
    const { router } = renderAt('/o/acme/groups/g1');
    const variables = await screen.findByRole('tab', { name: /^Variables/ });
    expect(variables).toHaveAttribute('tabindex', '0');
    expect(tab('Members')).toHaveAttribute('tabindex', '-1');
    variables.focus();
    const entries = router.history.length;
    await user.keyboard('{ArrowRight}');
    expect(tab('Members')).toHaveFocus();
    expect(router.state.location.search).toEqual({ tab: 'members' });
    await user.keyboard('{End}');
    expect(tab('Inventory')).toHaveFocus();
    await user.keyboard('{ArrowRight}');
    expect(tab('Variables')).toHaveFocus();
    await user.keyboard('{ArrowLeft}');
    expect(tab('Inventory')).toHaveFocus();
    await user.keyboard('{Home}');
    expect(tab('Variables')).toHaveFocus();
    expect(router.history.length).toBe(entries);
    await user.click(tab('Inheritance'));
    expect(router.history.length).toBe(entries + 1);
    expect(await panel().findByText(/nothing to compare/)).toBeInTheDocument();
  });

  it('opens the tab the URL names, and Variables for one it does not know', async () => {
    mockApi(routes());
    const { unmount } = renderAt('/o/acme/groups/g1?tab=inventory');
    await screen.findByRole('tablist');
    expect(selectedTab()).toBe('Inventory');
    unmount();
    renderAt('/o/acme/groups/g1?tab=nope');
    await screen.findByRole('tablist');
    expect(selectedTab()).toBe('Variables');
  });

  it('members: rows open the host and their menu does not navigate', async () => {
    const user = userEvent.setup();
    mockApi(routes());
    const { router } = renderAt('/o/acme/groups/g1?tab=members');
    const row = await screen.findByRole('link', { name: 'web-01' });
    expect(row).toHaveAttribute('href', '/o/acme/hosts/h1');
    expect(within(row).queryAllByRole('link')).toEqual([]);
    expect(within(row).queryAllByRole('button')).toEqual([]);

    await user.click(screen.getByRole('button', { name: 'Actions for web-01' }));
    await user.click(screen.getByRole('menuitem', { name: 'Edit values' }));
    const dialog = await screen.findByRole('dialog', { name: 'Values of web-01' });
    expect(within(dialog).getByLabelText('domain')).toHaveValue('web-01.acme.io');
    expect(router.state.location.pathname).toBe('/o/acme/groups/g1');
  });

  it('members: opens the host from the member menu', async () => {
    const user = userEvent.setup();
    mockApi(routes());
    const { router } = renderAt('/o/acme/groups/g1?tab=members');
    await user.click(await screen.findByRole('button', { name: 'Actions for web-01' }));
    await user.click(screen.getByRole('menuitem', { name: 'Open host' }));
    expect(await screen.findByText('HOST')).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/o/acme/hosts/h1');
  });

  it('subgroups: rows to their own page, without a menu', async () => {
    mockApi(routes());
    renderAt('/o/acme/groups/g1?tab=subgroups');
    const child = await screen.findByRole('link', { name: 'web_eu' });
    expect(child).toHaveAttribute('href', '/o/acme/groups/c1');
    expect(screen.queryByRole('button', { name: 'Actions for web_eu' })).not.toBeInTheDocument();
    expect(panel().getByText(/inherit this group's values/)).toBeInTheDocument();
  });

  it('inventory: the preview in the JSON viewer, the hosts folded', async () => {
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/preview': {
          inventory: { all: { children: ['prod'] }, web: { vars: { token: '********' } }, _meta: { hostvars: { 'web-01': {} } } },
          connections: {},
          warnings: [],
        },
      }),
    );
    renderAt('/o/acme/groups/g1?tab=inventory');
    const json = await screen.findByRole('region', { name: 'Inventory JSON' });
    expect(within(json).getByText('"********"')).toHaveAttribute('data-token', 'masked');
    expect(within(json).getByRole('button', { name: 'Expand or collapse _meta.hostvars' })).toHaveAttribute('aria-expanded', 'false');
    expect(panel().getByText(/Secrets are masked here/)).toBeInTheDocument();
  });

  it('stands a hint in for group values when the schema has no group field, with the way to the fields', async () => {
    const user = userEvent.setup();
    const hostOnly = { fields: [{ name: 'domain', type: 'string', scope: 'host', required: true }] };
    mockApi(routes('admin', { 'GET /api/v1/orgs/acme/inventory/groups/g1': group({ schema: hostOnly, vars: {} }) }));
    renderAt('/o/acme/groups/g1');
    const hint = await screen.findByRole('note', { name: 'No group fields' });
    expect(screen.queryByRole('button', { name: 'Save values' })).not.toBeInTheDocument();
    await user.click(within(hint).getByRole('button', { name: 'Manage fields …' }));
    expect(await screen.findByRole('dialog', { name: 'Manage fields' })).toBeInTheDocument();
  });

  it('offers no way to the fields from the hint to a viewer', async () => {
    const hostOnly = { fields: [{ name: 'domain', type: 'string', scope: 'host', required: true }] };
    mockApi(routes('viewer', { 'GET /api/v1/orgs/acme/inventory/groups/g1': group({ schema: hostOnly, vars: {} }) }));
    renderAt('/o/acme/groups/g1');
    const hint = await screen.findByRole('note', { name: 'No group fields' });
    expect(within(hint).queryByRole('button')).not.toBeInTheDocument();
  });

  it('no clipped cards', async () => {
    mockApi(routes());
    const { container } = renderAt('/o/acme/groups/g1');
    await screen.findByLabelText('env');
    expect(clippedElements(container)).toEqual([]);
  });
});

describe('GroupDetailPage links from before the tabs', () => {
  it('opens the tab an old ?tab= link names', async () => {
    mockApi(routes());
    renderAt('/o/acme/groups/g1?tab=children');
    expect(await screen.findByRole('link', { name: 'web_eu' })).toBeInTheDocument();
    expect(selectedTab()).toBe('Subgroups');
  });

  it('opens the fields for an old ?tab=schema link, for a writer', async () => {
    mockApi(routes());
    const { router } = renderAt('/o/acme/groups/g1?tab=schema', { browser: true });
    expect(await screen.findByRole('dialog', { name: 'Manage fields' })).toBeInTheDocument();
    expect(selectedTab()).toBe('Variables');
    expect(router.state.location.search).toEqual({});
  });

  it('leaves a reader on Variables for an old ?tab=schema link', async () => {
    mockApi(routes('viewer'));
    const { router } = renderAt('/o/acme/groups/g1?tab=schema', { browser: true });
    expect(await screen.findByLabelText('env')).toBeInTheDocument();
    expect(screen.queryByRole('dialog', { name: 'Fields' })).not.toBeInTheDocument();
    expect(selectedTab()).toBe('Variables');
    await waitFor(() => expect(router.state.location.hash).toBe(''));
  });

  it('drops an old #values anchor', async () => {
    mockApi(routes());
    const { router } = renderAt('/o/acme/groups/g1#values', { browser: true });
    expect(await screen.findByLabelText('env')).toBeInTheDocument();
    expect(selectedTab()).toBe('Variables');
    expect(router.state.location.hash).toBe('');
  });

  it('sends an old section anchor on to its tab', async () => {
    mockApi(routes());
    const { router } = renderAt('/o/acme/groups/g1#members', { browser: true });
    expect(await screen.findByRole('link', { name: 'web-01' })).toBeInTheDocument();
    expect(router.state.location.search).toEqual({ tab: 'members' });
    expect(router.state.location.hash).toBe('');
  });
});
