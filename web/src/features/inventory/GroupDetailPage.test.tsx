import { describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { GroupDetailPage } from './GroupDetailPage.tsx';

const SCHEMA = {
  fields: [
    { name: 'env', type: 'enum', options: ['prod', 'staging'], required: true },
    { name: 'db_password', type: 'secret' },
    { name: 'php_default', type: 'enum', options: ['8.3', '8.4'], default: '8.3', overridable: true },
    { name: 'domain', type: 'string', scope: 'host', required: true },
  ],
};

const GROUP = {
  group: {
    id: 'g1', name: 'webserver', description: '', parent_id: null, revision: 3, schema: SCHEMA,
    vars: { env: 'prod', db_password: { $secret: 'set' }, removed: 1 }, problems: [], orphaned: ['removed'],
    created_at: '', updated_at: '',
  },
};

const GROUPS = { groups: [{ id: 'g1', name: 'webserver', description: '', parent_id: null, members: 1, group_fields: 3, host_fields: 1, complete: true, revision: 3 }] };

const MEMBER = {
  group_id: 'g1', group_name: 'webserver', host_id: 'h1', host_name: 'web-01',
  vars: { domain: 'shop.acme.io' }, complete: true, problems: [], orphaned: [], revision: 7,
};

function renderAt(path: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const detail = createRoute({
    getParentRoute: () => orgRoute,
    path: '/groups/$id',
    validateSearch: (s: Record<string, unknown>) => ({ tab: typeof s.tab === 'string' ? s.tab : undefined }),
    component: GroupDetailPage,
  });
  const list = createRoute({ getParentRoute: () => orgRoute, path: '/groups', component: () => <div>GROUPS</div> });
  const host = createRoute({ getParentRoute: () => orgRoute, path: '/hosts/$id', component: () => <div>HOST</div> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([detail, list, host])]),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

const base = {
  'GET /api/v1/orgs/acme/inventory/groups/g1': GROUP,
  'GET /api/v1/orgs/acme/inventory/groups': GROUPS,
  'GET /api/v1/orgs/acme/bastions': { bastions: [] },
};

describe('GroupDetailPage', () => {
  it('opens on the schema tab and switches tabs', async () => {
    const user = userEvent.setup();
    mockApi(base);
    renderAt('/o/acme/groups/g1');
    expect(await screen.findByRole('list', { name: 'Fields' })).toBeInTheDocument();
    await user.click(screen.getByRole('tab', { name: 'Group values' }));
    expect(await screen.findByLabelText('env')).toBeInTheDocument();
    expect(screen.getByText(/Values of removed fields are still stored/)).toBeInTheDocument();
  });

  it('saves group values with If-Match, a keep marker for the stored secret, and without orphans', async () => {
    const user = userEvent.setup();
    let seen: MockRequest | undefined;
    mockApi({
      ...base,
      'PUT /api/v1/orgs/acme/inventory/groups/g1/vars': (req: MockRequest) => {
        seen = req;
        return GROUP;
      },
    });
    renderAt('/o/acme/groups/g1?tab=values');
    await user.selectOptions(await screen.findByLabelText('env'), 'staging');
    await user.click(screen.getByRole('button', { name: 'Save values' }));
    await screen.findByText('Group values saved');
    expect(seen?.headers.get('If-Match')).toBe('"3"');
    expect(seen?.body).toEqual({ vars: { env: 'staging', db_password: { $secret: 'keep', from: 'db_password' } } });
  });

  it('shows the server problems of a rejected save at their fields', async () => {
    const user = userEvent.setup();
    mockApi({
      ...base,
      'PUT /api/v1/orgs/acme/inventory/groups/g1/vars': () =>
        new Response(JSON.stringify({ status: 422, errors: [{ location: 'body.vars.env', message: 'must be one of prod, staging' }] }), {
          status: 422, headers: { 'Content-Type': 'application/json' },
        }),
    });
    renderAt('/o/acme/groups/g1?tab=values');
    await user.click(await screen.findByRole('button', { name: 'Save values' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('must be one of prod, staging');
  });

  it('offers a reload when the schema changed underneath', async () => {
    const user = userEvent.setup();
    mockApi({
      ...base,
      'PUT /api/v1/orgs/acme/inventory/groups/g1/schema': () =>
        new Response(JSON.stringify({ status: 412, detail: 'changed' }), { status: 412, headers: { 'Content-Type': 'application/json' } }),
    });
    renderAt('/o/acme/groups/g1');
    await user.click(await screen.findByRole('button', { name: 'Save schema' }));
    expect(await screen.findByText(/changed since you loaded it/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Reload' })).toBeInTheDocument();
  });

  it('assigns a managed host through the generated form', async () => {
    const user = userEvent.setup();
    let put: MockRequest | undefined;
    mockApi({
      ...base,
      'GET /api/v1/orgs/acme/inventory/groups/g1/members': {
        members: [{ group_id: 'g1', group_name: 'webserver', host_id: 'h1', host_name: 'web-01', vars: {}, complete: false, problems: [{ path: 'domain', message: 'is required' }], orphaned: [], revision: 1 }],
      },
      'GET /api/v1/orgs/acme/hosts': {
        hosts: [
          { id: 'h1', name: 'web-01', zone_id: 'z', address: '', managed: true, monitored: false, facts: {}, created_at: '' },
          { id: 'h2', name: 'web-02', zone_id: 'z', address: '', managed: true, monitored: false, facts: {}, created_at: '' },
        ],
        next_cursor: '',
      },
      'PUT /api/v1/orgs/acme/inventory/groups/g1/members/h2': (req: MockRequest) => {
        put = req;
        return { member: { group_id: 'g1', group_name: 'webserver', host_id: 'h2', host_name: 'web-02', vars: {}, complete: true, problems: [], orphaned: [], revision: 1 } };
      },
    });
    renderAt('/o/acme/groups/g1?tab=members');
    expect(await screen.findByText('1 to fill in')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Assign host' }));
    const dialog = screen.getByRole('dialog');
    const hostSelect = within(dialog).getByLabelText('Host');
    expect(within(hostSelect).queryByRole('option', { name: 'web-01' })).not.toBeInTheDocument();
    await user.selectOptions(hostSelect, 'h2');
    await user.type(within(dialog).getByLabelText('domain'), 'shop.acme.io');
    // The read-only group values below the form also have a php_default; the override comes first.
    await user.selectOptions(within(dialog).getAllByLabelText('php_default')[0], '8.4');
    await user.click(within(dialog).getByRole('button', { name: 'Assign to webserver' }));
    await screen.findByText('Host assigned to webserver');
    expect(put?.body).toEqual({ vars: { domain: 'shop.acme.io', php_default: '8.4' } });
    expect(put?.headers.get('If-Match')).toBeNull();
  });

  it('reaches a managed host beyond the first page by searching', async () => {
    const user = userEvent.setup();
    const searches: string[] = [];
    let put: MockRequest | undefined;
    const host = (id: string, name: string) => ({ id, name, zone_id: 'z', address: '', managed: true, monitored: false, facts: {}, created_at: '' });
    mockApi({
      ...base,
      'GET /api/v1/orgs/acme/inventory/groups/g1/members': { members: [MEMBER] },
      'GET /api/v1/orgs/acme/hosts': (req: MockRequest) => {
        expect(req.search.get('managed')).toBe('true');
        const q = req.search.get('q') ?? '';
        searches.push(q);
        if (q === 'web-77') return { hosts: [host('h77', 'web-77')], next_cursor: '' };
        return { hosts: [host('h1', 'web-01'), host('h2', 'web-02')], next_cursor: 'page-2' };
      },
      'PUT /api/v1/orgs/acme/inventory/groups/g1/members/h77': (req: MockRequest) => {
        put = req;
        return { member: { ...MEMBER, host_id: 'h77', host_name: 'web-77' } };
      },
    });
    renderAt('/o/acme/groups/g1?tab=members');
    await user.click(await screen.findByRole('button', { name: 'Assign host' }));
    const dialog = screen.getByRole('dialog');
    expect(await within(dialog).findByRole('option', { name: 'web-02' })).toBeInTheDocument();
    expect(within(dialog).getByText(/More managed hosts match/)).toBeInTheDocument();
    expect(within(dialog).queryByText(/already a member/)).not.toBeInTheDocument();

    await user.type(within(dialog).getByRole('searchbox', { name: 'Search managed hosts' }), 'web-77');
    await user.selectOptions(within(dialog).getByLabelText('Host'), await within(dialog).findByRole('option', { name: 'web-77' }));
    expect(within(dialog).queryByText(/More managed hosts match/)).not.toBeInTheDocument();
    await user.type(within(dialog).getByLabelText('domain'), 'far.acme.io');
    await user.click(within(dialog).getByRole('button', { name: 'Assign to webserver' }));
    await screen.findByText('Host assigned to webserver');
    expect(searches).toContain('web-77');
    expect(put?.body).toEqual({ vars: { domain: 'far.acme.io' } });
  });

  it('says every managed host is a member only when no page holds another one', async () => {
    const user = userEvent.setup();
    const host = (id: string, name: string) => ({ id, name, zone_id: 'z', address: '', managed: true, monitored: false, facts: {}, created_at: '' });
    let pages: Record<string, { hosts: ReturnType<typeof host>[]; next_cursor: string }> = {
      '': { hosts: [host('h1', 'web-01')], next_cursor: 'page-2' },
      'page-2': { hosts: [host('h2', 'web-02')], next_cursor: '' },
    };
    mockApi({
      ...base,
      'GET /api/v1/orgs/acme/inventory/groups/g1/members': { members: [MEMBER] },
      'GET /api/v1/orgs/acme/hosts': (req: MockRequest) => pages[req.search.get('cursor') ?? ''],
    });
    const { unmount } = renderAt('/o/acme/groups/g1?tab=members');
    await user.click(await screen.findByRole('button', { name: 'Assign host' }));
    let dialog = screen.getByRole('dialog');
    // The first page holds only the member; the second page is fetched for the next candidate.
    expect(await within(dialog).findByRole('option', { name: 'web-02' })).toBeInTheDocument();
    expect(within(dialog).queryByText(/already a member/)).not.toBeInTheDocument();
    unmount();

    pages = { '': { hosts: [host('h1', 'web-01')], next_cursor: '' } };
    renderAt('/o/acme/groups/g1?tab=members');
    await user.click(await screen.findByRole('button', { name: 'Assign host' }));
    dialog = screen.getByRole('dialog');
    expect(await within(dialog).findByText('Every managed host is already a member.')).toBeInTheDocument();
  });

  it('renders the masked preview and its warnings', async () => {
    const user = userEvent.setup(); // installs a clipboard stub on navigator
    mockApi({
      ...base,
      'GET /api/v1/orgs/acme/inventory/preview': {
        inventory: { all: { children: ['webserver'] }, webserver: { vars: { db_password: '********' } } },
        connections: {},
        warnings: [{ host: 'web-03', message: 'groups dmz and edge set different jump hosts; dmz wins' }],
      },
    });
    renderAt('/o/acme/groups/g1?tab=preview');
    expect(await screen.findByLabelText('Inventory JSON')).toHaveTextContent('"db_password": "********"');
    expect(screen.getByText(/dmz wins/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Copy' }));
    expect(await navigator.clipboard.readText()).toContain('"webserver"');
  });

  it('saves settings with the jump host and reports a refused delete', async () => {
    const user = userEvent.setup();
    let patched: MockRequest | undefined;
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    mockApi({
      ...base,
      'GET /api/v1/orgs/acme/bastions': { bastions: [{ id: 'b1', name: 'bastion-fra', address: 'b.example', port: 22, username: 'ops' }] },
      'PATCH /api/v1/orgs/acme/inventory/groups/g1': (req: MockRequest) => {
        patched = req;
        return GROUP;
      },
      'DELETE /api/v1/orgs/acme/inventory/groups/g1': () =>
        new Response(JSON.stringify({ status: 409, detail: 'the group still has child groups; move or delete them first' }), {
          status: 409, headers: { 'Content-Type': 'application/json' },
        }),
    });
    renderAt('/o/acme/groups/g1?tab=settings');
    await user.selectOptions(await screen.findByLabelText('Jump host'), 'b1');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await screen.findByText('Group saved');
    expect(patched?.headers.get('If-Match')).toBe('"3"');
    expect(patched?.body).toEqual({ name: 'webserver', bastion_id: 'b1' });
    await user.click(screen.getByRole('button', { name: 'Delete group' }));
    expect(await screen.findByText(/still has child groups/)).toBeInTheDocument();
  });

  it('opens the edit dialog prefilled and sends If-Match with the member revision on save', async () => {
    const user = userEvent.setup();
    let put: MockRequest | undefined;
    mockApi({
      ...base,
      'GET /api/v1/orgs/acme/inventory/groups/g1/members': { members: [MEMBER] },
      'PUT /api/v1/orgs/acme/inventory/groups/g1/members/h1': (req: MockRequest) => {
        put = req;
        return { member: { ...MEMBER, revision: 8 } };
      },
    });
    renderAt('/o/acme/groups/g1?tab=members');
    await user.click(await screen.findByRole('button', { name: 'Edit values of web-01' }));
    const dialog = await screen.findByRole('dialog', { name: 'Values of web-01' });
    expect(within(dialog).getByLabelText('domain')).toHaveValue('shop.acme.io');
    await user.click(within(dialog).getByRole('button', { name: 'Save values' }));
    await screen.findByText('Values of web-01 saved');
    expect(put?.headers.get('If-Match')).toBe('"7"');
  });

  it('reports a failed member removal instead of rejecting silently', async () => {
    const user = userEvent.setup();
    const originalConfirm = window.confirm;
    window.confirm = () => true;
    mockApi({
      ...base,
      'GET /api/v1/orgs/acme/inventory/groups/g1/members': { members: [MEMBER] },
      'DELETE /api/v1/orgs/acme/inventory/groups/g1/members/h1': () =>
        new Response(JSON.stringify({ detail: 'host no longer exists' }), { status: 404 }),
    });
    renderAt('/o/acme/groups/g1?tab=members');
    await user.click(await screen.findByRole('button', { name: 'Remove web-01' }));

    const toast = await screen.findByRole('status');
    expect(toast).toHaveTextContent('host no longer exists');
    window.confirm = originalConfirm;
  });
});
