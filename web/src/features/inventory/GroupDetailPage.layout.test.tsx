import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  createBrowserHistory,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  redirect,
  RouterProvider,
} from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { clippedElements } from '../../test/clipped.ts';
import { GroupDetailPage } from './GroupDetailPage.tsx';

const SCHEMA = {
  fields: [
    { name: 'env', type: 'string', required: true },
    { name: 'domain', type: 'string', scope: 'host', required: true },
  ],
};

const group = (extra = {}) => ({
  group: {
    id: 'g1', name: 'web', description: 'Front web servers', parent_id: 'p1', bastion_id: 'b1', revision: 3, schema: SCHEMA,
    vars: { env: 'prod' }, problems: [], orphaned: [], created_at: '', updated_at: '', ...extra,
  },
});

const summary = (id: string, name: string, parent_id: string | null, extra = {}) => ({
  id, name, description: '', parent_id, members: 0, group_fields: 1, host_fields: 0, complete: true, revision: 1, ...extra,
});

const GROUPS = {
  groups: [
    summary('p1', 'prod', null),
    summary('g1', 'web', 'p1', { members: 2, group_fields: 1, host_fields: 1, complete: false, bastion_id: 'b1', revision: 3 }),
    summary('c1', 'web_eu', 'g1', { members: 4 }),
  ],
};

const member = (host_id: string, host_name: string, extra = {}) => ({
  group_id: 'g1', group_name: 'web', host_id, host_name, vars: { domain: `${host_name}.acme.io` }, complete: true, problems: [], orphaned: [], revision: 7, ...extra,
});

const MEMBERS = {
  members: [member('h1', 'web-01'), member('h2', 'web-02', { vars: {}, complete: false, problems: [{ path: 'domain', message: 'is required' }] })],
};

const org = (role: string) => ({ org: { id: 'o1', name: 'Acme', slug: 'acme', role, created_at: '2026-01-01T00:00:00Z' } });

const routes = (role = 'admin', extra: Record<string, unknown> = {}) => ({
  'GET /api/v1/orgs/acme': org(role),
  'GET /api/v1/orgs/acme/inventory/groups/g1': group(),
  'GET /api/v1/orgs/acme/inventory/groups': GROUPS,
  'GET /api/v1/orgs/acme/inventory/groups/g1/members': MEMBERS,
  'GET /api/v1/orgs/acme/inventory/preview': { inventory: { all: { children: ['web'] } }, connections: {}, warnings: [] },
  'GET /api/v1/orgs/acme/bastions': { bastions: [{ id: 'b1', name: 'bastion-fra', address: 'b.example', port: 22, username: 'ops' }] },
  'GET /api/v1/orgs/acme/credentials': { credentials: [] },
  ...extra,
});

/** The section an old `?tab=` link names, as the app router maps it. */
const TAB_SECTION: Record<string, string> = { members: 'members', values: 'values', schema: 'schema', children: 'children', preview: 'preview' };

function renderAt(path: string, { browser = false } = {}) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const detail = createRoute({
    getParentRoute: () => orgRoute,
    path: '/groups/$id',
    validateSearch: (s: Record<string, unknown>) => ({ tab: typeof s.tab === 'string' ? s.tab : undefined }),
    beforeLoad: ({ search, params }) => {
      if (search.tab) {
        throw redirect({ to: '/o/$org/groups/$id', params, search: { tab: undefined }, hash: Object.hasOwn(TAB_SECTION, search.tab) ? TAB_SECTION[search.tab] : undefined, replace: true });
      }
    },
    component: GroupDetailPage,
  });
  const list = createRoute({ getParentRoute: () => orgRoute, path: '/groups', component: () => <div>GROUPS</div> });
  const host = createRoute({ getParentRoute: () => orgRoute, path: '/hosts/$id', component: () => <div>HOST</div> });
  if (browser) window.history.replaceState(null, '', path);
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([detail, list, host])]),
    history: browser ? createBrowserHistory() : createMemoryHistory({ initialEntries: [path] }),
  });
  const view = render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
  return { ...view, router };
}

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

    const nav = screen.getByRole('navigation');
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
    await within(dialog).findByLabelText('Jump host');
    await pickOption(within(dialog), 'Jump host', { value: 'b1' });
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    await screen.findByText('Group saved');
    expect(patched?.headers.get('If-Match')).toBe('"3"');
    expect(patched?.body).toEqual({ name: 'web_fe', description: 'Front web servers', parent_id: 'p1', bastion_id: 'b1' });
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
    for (const name of ['Edit', 'Assign host', 'Add child group', 'Save schema', 'Save values', 'Discard', 'More actions for web']) {
      expect(screen.queryByRole('button', { name })).not.toBeInTheDocument();
    }
    await user.click(screen.getByRole('button', { name: 'Actions for web-01' }));
    expect(screen.getAllByRole('menuitem').map((m) => m.textContent)).toEqual(['Open host']);
    await user.keyboard('{Escape}');

    expect(screen.getByRole('button', { name: 'Add field' })).toBeDisabled();
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

  it('keeps an unsaved schema draft when the group values are saved', async () => {
    const user = userEvent.setup();
    let current = group();
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': () => current,
        'PUT /api/v1/orgs/acme/inventory/groups/g1/vars': (req: MockRequest) => {
          current = group({ revision: 4, vars: (req.body as { vars: Record<string, unknown> }).vars });
          return current;
        },
      }),
    );
    renderAt('/o/acme/groups/g1');
    const fields = await screen.findByRole('list', { name: 'Fields' });
    const before = within(fields).getAllByRole('listitem').length;
    await user.click(screen.getByRole('button', { name: 'Add field' }));
    expect(within(screen.getByRole('list', { name: 'Fields' })).getAllByRole('listitem')).toHaveLength(before + 1);

    await user.clear(screen.getByLabelText('env'));
    await user.type(screen.getByLabelText('env'), 'staging');
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await screen.findByText('Group values saved');
    await waitFor(() => expect(screen.getByLabelText('env')).toHaveValue('staging'));
    // The refetched group (revision 4) leaves the unsaved schema draft alone.
    await waitFor(() => expect(current.group.revision).toBe(4));
    expect(within(screen.getByRole('list', { name: 'Fields' })).getAllByRole('listitem')).toHaveLength(before + 1);
  });

  it('keeps unsaved values when the group is renamed', async () => {
    const user = userEvent.setup();
    let current = group();
    let refetched = false;
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/groups/g1': () => {
          if (current.group.revision === 4) refetched = true;
          return current;
        },
        'PATCH /api/v1/orgs/acme/inventory/groups/g1': (req: MockRequest) => {
          current = group({ revision: 4, name: (req.body as { name: string }).name });
          return current;
        },
      }),
    );
    renderAt('/o/acme/groups/g1');
    const env = await screen.findByLabelText('env');
    await user.clear(env);
    await user.type(env, 'typed but not saved');

    await user.click(screen.getByRole('button', { name: 'Edit' }));
    const dialog = await screen.findByRole('dialog', { name: 'Edit group' });
    await user.clear(within(dialog).getByLabelText('Name'));
    await user.type(within(dialog).getByLabelText('Name'), 'web_fe');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    expect(await screen.findByRole('heading', { level: 1, name: 'web_fe' })).toBeInTheDocument();
    expect(refetched).toBe(true);
    expect(screen.getByLabelText('env')).toHaveValue('typed but not saved');
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
