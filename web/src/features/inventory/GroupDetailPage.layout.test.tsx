import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { clippedElements } from '../../test/clipped.ts';
import { group, routes, renderAt } from './groupPageTestKit.tsx';

afterEach(() => {
  vi.restoreAllMocks();
  window.history.replaceState(null, '', '/');
});

describe('GroupDetailPage layout', () => {
  it('shows the group in the sidebar and every section in one column', async () => {
    mockApi(routes());
    renderAt('/o/acme/groups/g1');
    expect(await screen.findByRole('heading', { level: 1, name: 'web' })).toBeInTheDocument();
    expect(await screen.findByText('Incomplete')).toBeInTheDocument();
    expect(screen.getByText('Front web servers')).toBeInTheDocument();

    const fact = (label: string) => screen.getByText(label, { selector: 'dt' }).nextElementSibling;
    expect(fact('Parent')).toHaveTextContent('prod');
    expect(within(fact('Parent') as HTMLElement).getByRole('link', { name: 'prod' })).toHaveAttribute('href', '/o/acme/groups/p1');
    await waitFor(() => expect(fact('Members')).toHaveTextContent('2, 1 to fill in'));
    expect(fact('Child groups')).toHaveTextContent('1');
    expect(fact('Fields')).toHaveTextContent('1 group · 1 host');
    expect(fact('Jump host')).toHaveTextContent('bastion-fra');

    const nav = screen.getByRole('navigation', { name: 'Sections' });
    for (const [id, name] of [
      ['members', 'Members'],
      ['values', 'Group values'],
      ['schema', 'Variable schema'],
      ['children', 'Child groups'],
      ['preview', 'Preview inventory'],
    ]) {
      expect(within(nav).getByRole('link', { name: new RegExp(`^${name}`) })).toHaveAttribute('href', `#${id}`);
      expect(screen.getByRole('region', { name })).toHaveAttribute('id', id);
    }
    expect(within(nav).getByRole('link', { name: /^Members/ })).toHaveTextContent('2');
    expect(within(nav).getByRole('link', { name: /^Child groups/ })).toHaveTextContent('1');
    expect(screen.queryAllByRole('tab')).toEqual([]);
    expect(screen.queryByRole('tablist')).not.toBeInTheDocument();
  });

  it('tells a jump host still loading from none', async () => {
    let answer: (value: unknown) => void = () => {};
    mockApi(routes('admin', { 'GET /api/v1/orgs/acme/bastions': () => new Promise((resolve) => (answer = resolve)) }));
    const { unmount } = renderAt('/o/acme/groups/g1');
    const fact = async () => (await screen.findByText('Jump host', { selector: 'dt' })).nextElementSibling;
    expect(await fact()).toHaveTextContent('Loading…');
    answer({ bastions: [{ id: 'b1', name: 'bastion-fra', address: 'b.example', port: 22, username: 'ops' }] });
    await waitFor(async () => expect(await fact()).toHaveTextContent('bastion-fra'));
    unmount();

    mockApi(routes('admin', { 'GET /api/v1/orgs/acme/inventory/groups/g1': group({ bastion_id: undefined }) }));
    renderAt('/o/acme/groups/g1');
    await screen.findByRole('link', { name: 'web-01' });
    expect(await fact()).toHaveTextContent('—');
  });

  it('member rows open the host and their menu does not navigate', async () => {
    const user = userEvent.setup();
    mockApi(routes());
    const { router } = renderAt('/o/acme/groups/g1');
    const row = await screen.findByRole('link', { name: 'web-01' });
    expect(row).toHaveAttribute('href', '/o/acme/hosts/h1');
    expect(within(row).queryAllByRole('link')).toEqual([]);
    expect(within(row).queryAllByRole('button')).toEqual([]);

    await user.click(screen.getByRole('button', { name: 'Actions for web-01' }));
    await user.click(screen.getByRole('menuitem', { name: 'Edit values' }));
    const dialog = await screen.findByRole('dialog', { name: 'Values of web-01' });
    expect(within(dialog).getByLabelText('domain')).toHaveValue('web-01.acme.io');
    expect(router.state.location.pathname).toBe('/o/acme/groups/g1');

    // Child groups are rows to their own page, without a menu.
    const child = screen.getByRole('link', { name: 'web_eu' });
    expect(child).toHaveAttribute('href', '/o/acme/groups/c1');
    expect(screen.queryByRole('button', { name: 'Actions for web_eu' })).not.toBeInTheDocument();
  });

  it('opens the host from the member menu', async () => {
    const user = userEvent.setup();
    mockApi(routes());
    const { router } = renderAt('/o/acme/groups/g1');
    await user.click(await screen.findByRole('button', { name: 'Actions for web-01' }));
    await user.click(screen.getByRole('menuitem', { name: 'Open host' }));
    expect(await screen.findByText('HOST')).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/o/acme/hosts/h1');
  });

  it('edits name, parent and jump host in a dialog and deletes from the sidebar menu', async () => {
    const user = userEvent.setup();
    let patched: MockRequest | undefined;
    let deleted = false;
    vi.spyOn(window, 'confirm').mockReturnValue(true);
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
    await user.click(await screen.findByRole('button', { name: 'Edit' }));
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
    await user.click(screen.getByRole('menuitem', { name: 'Delete group…' }));
    expect(window.confirm).toHaveBeenCalledWith('Delete group web? Its members lose the values they have for this group.');
    expect(await screen.findByText('GROUPS')).toBeInTheDocument();
    expect(deleted).toBe(true);
    expect(router.state.location.pathname).toBe('/o/acme/groups');
  });

  it('reports a refused delete and stays on the group', async () => {
    const user = userEvent.setup();
    vi.spyOn(window, 'confirm').mockReturnValue(true);
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
    await user.click(screen.getByRole('menuitem', { name: 'Delete group…' }));
    expect(await screen.findByText(/still has child groups/)).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/o/acme/groups/g1');
  });

  it('hides every write action from a viewer', async () => {
    const user = userEvent.setup();
    mockApi(routes('viewer'));
    renderAt('/o/acme/groups/g1');
    await screen.findByRole('link', { name: 'web-01' });
    for (const name of ['Edit', 'Assign host', 'Add child group', 'Save schema', 'Save values', 'Discard', 'Add field', 'More actions for web']) {
      expect(screen.queryByRole('button', { name })).not.toBeInTheDocument();
    }
    await user.click(screen.getByRole('button', { name: 'Actions for web-01' }));
    expect(screen.getAllByRole('menuitem').map((m) => m.textContent)).toEqual(['Open host']);
    await user.keyboard('{Escape}');

    // The schema editor is read-only: no field can be added, switched or
    // moved, but a field still opens, in a dialog that saves nothing.
    expect(screen.getByRole('switch', { name: 'env active' })).toBeDisabled();
    await user.click(screen.getByRole('button', { name: /^env\b/ }));
    const field = await screen.findByRole('dialog', { name: 'Field env' });
    expect(within(field).getByLabelText('Name')).toBeDisabled();
    expect(within(field).queryByRole('button', { name: 'Done' })).not.toBeInTheDocument();
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(screen.getByLabelText('env')).toBeDisabled();
  });

  it('stands a hint in for group values when the schema has no group field', async () => {
    const hostOnly = { fields: [{ name: 'domain', type: 'string', scope: 'host', required: true }] };
    mockApi(routes('admin', { 'GET /api/v1/orgs/acme/inventory/groups/g1': group({ schema: hostOnly, vars: {} }) }));
    renderAt('/o/acme/groups/g1');
    const values = await screen.findByRole('region', { name: 'Group values' });
    expect(await within(values).findByText('No group fields')).toBeInTheDocument();
    expect(within(values).getByRole('link', { name: 'Go to schema' }).getAttribute('href')).toMatch(/#schema$/);
    expect(within(values).queryByRole('button', { name: 'Save values' })).not.toBeInTheDocument();
  });

  it('offers no way to the schema from the hint to a viewer', async () => {
    const hostOnly = { fields: [{ name: 'domain', type: 'string', scope: 'host', required: true }] };
    mockApi(routes('viewer', { 'GET /api/v1/orgs/acme/inventory/groups/g1': group({ schema: hostOnly, vars: {} }) }));
    renderAt('/o/acme/groups/g1');
    const values = await screen.findByRole('region', { name: 'Group values' });
    expect(await within(values).findByText('No group fields')).toBeInTheDocument();
    expect(within(values).queryByRole('link', { name: 'Go to schema' })).not.toBeInTheDocument();
  });

  it('no clipped cards', async () => {
    mockApi(routes());
    const { container } = renderAt('/o/acme/groups/g1');
    await screen.findByRole('link', { name: 'web-01' });
    await screen.findByLabelText('Inventory JSON');
    expect(clippedElements(container)).toEqual([]);
  });
});

describe('GroupDetailPage old tab links', () => {
  const original = Element.prototype.scrollIntoView;
  afterEach(() => {
    Element.prototype.scrollIntoView = original;
  });

  it('lands an old ?tab= link on its section once the group has loaded', async () => {
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    mockApi(routes());
    const { router } = renderAt('/o/acme/groups/g1?tab=members', { browser: true });
    await screen.findByRole('link', { name: 'web-01' });
    expect(router.state.location.hash).toBe('members');
    expect(router.state.location.search).toEqual({});
    await waitFor(() => expect(scroll.mock.contexts).toContain(document.getElementById('members')));
  });

  it('still lands on the section when the redirect happens on the open page', async () => {
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    mockApi(routes());
    const { router } = renderAt('/o/acme/groups/g1', { browser: true });
    await screen.findByRole('link', { name: 'web-01' });
    expect(scroll).not.toHaveBeenCalled();

    await router.navigate({ to: '/o/$org/groups/$id', params: { org: 'acme', id: 'g1' }, search: { tab: 'preview' } });
    expect(router.state.location.hash).toBe('preview');
    await waitFor(() => expect(scroll.mock.contexts).toContain(document.getElementById('preview')));
  });
});
