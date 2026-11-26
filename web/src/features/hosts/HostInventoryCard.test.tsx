import { describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { acceptConfirm, findConfirmDialog } from '../../test/confirmDialog.ts';
import { ToastProvider } from '../../ui/toast.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { HostInventoryCard } from './HostInventoryCard.tsx';

function renderCard(managed: boolean, canWrite = false) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute({ component: () => <HostInventoryCard org="acme" hostId="h1" hostName="web-01" managed={managed} canWrite={canWrite} /> });
  const groupRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/o/$org/groups/$id',
    validateSearch: (s: Record<string, unknown>) => ({ tab: typeof s.tab === 'string' ? s.tab : undefined }),
  });
  const router = createRouter({ routeTree: rootRoute.addChildren([groupRoute]), history: createMemoryHistory({ initialEntries: ['/'] }) });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <ConfirmProvider>
          <RouterProvider router={router} />
        </ConfirmProvider>
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('HostInventoryCard', () => {
  it('explains that inventory groups need the Managed capability', async () => {
    mockApi({});
    renderCard(false);
    expect(await screen.findByText('Inventory groups need the Managed capability.')).toBeInTheDocument();
  });

  it('shows a loading line while the groups are read, then an error line when that fails', async () => {
    let fail: () => void = () => {};
    const gate = new Promise<void>((resolve) => { fail = resolve; });
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/inventory/groups': async () => {
        await gate;
        return new Response('{"detail":"boom"}', { status: 500 });
      },
    });
    renderCard(true);
    expect(await screen.findByText('Loading inventory groups…')).toBeInTheDocument();
    fail();
    expect(await screen.findByText('The inventory groups could not be loaded.')).toBeInTheDocument();
    expect(screen.queryByText('Loading inventory groups…')).not.toBeInTheDocument();
  });

  it('says when a managed host is in no group', async () => {
    mockApi({ 'GET /api/v1/orgs/acme/hosts/h1/inventory/groups': { groups: [] } });
    renderCard(true);
    expect(await screen.findByText('Not in any inventory group yet.')).toBeInTheDocument();
  });

  it('links each group to its Members tab and flags missing values', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/inventory/groups': {
        groups: [{ group_id: 'g1', group_name: 'webserver', host_id: 'h1', host_name: 'web-01', vars: {}, complete: false, problems: [{ path: 'domain', message: 'is required' }], orphaned: [], revision: 1 }],
      },
    });
    renderCard(true);
    const link = await screen.findByRole('link', { name: 'webserver' });
    expect(link.getAttribute('href')).toBe('/o/acme/groups/g1?tab=members');
    expect(screen.getByText('1 to fill in')).toBeInTheDocument();
  });

  it('flags the values an assistant wrote as plain text until a person changes or accepts them', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/inventory/groups': {
        groups: [
          { group_id: 'g1', group_name: 'webserver', host_id: 'h1', host_name: 'web-01', vars: {}, complete: true, problems: [], orphaned: [], assistant_keys: ['note', 'role'], revision: 1 },
          { group_id: 'g2', group_name: 'database', host_id: 'h1', host_name: 'web-01', vars: {}, complete: true, problems: [], orphaned: [], assistant_keys: [], revision: 1 },
        ],
      },
    });
    renderCard(true);
    const row = (await screen.findByRole('link', { name: 'webserver' })).closest('li')!;
    const pill = within(row).getByText('assistant values');
    expect(pill.closest('[title]')).toHaveAttribute('title', 'Written by an assistant: note, role — used as plain text until a person changes or accepts them');
    const other = screen.getByRole('link', { name: 'database' }).closest('li')!;
    expect(within(other).queryByText('assistant values')).not.toBeInTheDocument();
  });

  describe('managing memberships', () => {
    const summary = (id: string, name: string, parent_id: string | null = null) => ({
      id, name, description: '', parent_id, members: 0, group_fields: 1, host_fields: 1, complete: true, revision: 1,
    });
    const GROUPS = { groups: [summary('p1', 'prod'), summary('g1', 'webserver', 'p1'), summary('g2', 'database'), summary('g3', 'mail')] };
    const SCHEMA = { fields: [{ name: 'domain', type: 'string', scope: 'host', required: true }] };
    const full = (id: string, name: string) => ({
      group: { id, name, description: '', revision: 2, schema: SCHEMA, vars: {}, problems: [], orphaned: [], created_at: '', updated_at: '' },
    });
    const MEMBERSHIP = { group_id: 'g1', group_name: 'webserver', host_id: 'h1', host_name: 'web-01', vars: { domain: 'a.io' }, complete: true, problems: [], orphaned: [], revision: 4 };
    const routes = (extra: Record<string, unknown> = {}) => ({
      'GET /api/v1/orgs/acme/hosts/h1/inventory/groups': { groups: [MEMBERSHIP] },
      'GET /api/v1/orgs/acme/inventory/groups': GROUPS,
      'GET /api/v1/orgs/acme/inventory/groups/g1': full('g1', 'webserver'),
      'GET /api/v1/orgs/acme/inventory/groups/g2': full('g2', 'database'),
      ...extra,
    });

    it('shows no actions to a reader', async () => {
      mockApi(routes());
      renderCard(true, false);
      await screen.findByRole('link', { name: 'webserver' });
      expect(screen.queryByRole('button', { name: /Add to group/ })).not.toBeInTheDocument();
      expect(screen.queryByRole('button', { name: /Edit values/ })).not.toBeInTheDocument();
      expect(screen.queryByRole('button', { name: /Remove from group/ })).not.toBeInTheDocument();
    });

    it('lists inherited groups muted, with the subgroup they come through and no actions', async () => {
      mockApi(routes());
      renderCard(true, true);
      const item = (await screen.findByText('prod')).closest('li')!;
      expect(within(item).getByText('via webserver')).toBeInTheDocument();
      expect(within(item).queryByRole('button')).not.toBeInTheDocument();
    });

    it('offers only groups the host is not in and assigns to the chosen one', async () => {
      const user = userEvent.setup();
      let put: MockRequest | undefined;
      let release: () => void = () => {};
      const gate = new Promise<void>((resolve) => { release = resolve; });
      mockApi(routes({
        'GET /api/v1/orgs/acme/inventory/groups/g2': async () => {
          await gate;
          return full('g2', 'database');
        },
        'PUT /api/v1/orgs/acme/inventory/groups/g2/members/h1': (req: MockRequest) => {
          put = req;
          return { member: { ...MEMBERSHIP, group_id: 'g2', group_name: 'database' } };
        },
      }));
      renderCard(true, true);
      await user.click(await screen.findByRole('button', { name: 'Add to group…' }));
      const dialog = await screen.findByRole('dialog', { name: 'Add web-01 to a group' });
      await user.click(within(dialog).getByLabelText('Group'));
      expect(within(dialog).queryByRole('option', { name: 'webserver' })).not.toBeInTheDocument();
      expect(within(dialog).queryByRole('option', { name: 'all' })).not.toBeInTheDocument();
      expect(within(dialog).getByRole('option', { name: 'database' })).toBeInTheDocument();
      await user.click(within(dialog).getByRole('option', { name: 'database' }));
      // While the chosen group is read, the dialog says so.
      expect(within(dialog).getByRole('status')).toHaveTextContent('Loading');
      release();
      await user.type(await within(dialog).findByLabelText('domain'), 'db.io');
      await user.click(within(dialog).getByRole('button', { name: 'Assign to database' }));
      await screen.findByText('Host assigned to database');
      expect(put?.body).toEqual({ vars: { domain: 'db.io' } });
    });

    it('edits the values of a membership with its revision', async () => {
      const user = userEvent.setup();
      let put: MockRequest | undefined;
      mockApi(routes({
        'PUT /api/v1/orgs/acme/inventory/groups/g1/members/h1': (req: MockRequest) => {
          put = req;
          return { member: MEMBERSHIP };
        },
      }));
      renderCard(true, true);
      await user.click(await screen.findByRole('button', { name: 'Edit values in group webserver' }));
      const dialog = await screen.findByRole('dialog');
      const domain = await within(dialog).findByLabelText('domain');
      expect(domain).toHaveValue('a.io');
      await user.clear(domain);
      await user.type(domain, 'b.io');
      await user.click(within(dialog).getByRole('button', { name: 'Save values' }));
      await screen.findByText('Values of web-01 saved');
      expect(put?.body).toEqual({ vars: { domain: 'b.io' } });
      expect(put?.headers.get('If-Match')).toBe('"4"');
    });

    it('accepts a value an assistant wrote as live from the values dialog', async () => {
      const user = userEvent.setup();
      let accepted: MockRequest | undefined;
      mockApi(routes({
        'GET /api/v1/orgs/acme/hosts/h1/inventory/groups': { groups: [{ ...MEMBERSHIP, assistant_keys: ['domain'] }] },
        'POST /api/v1/orgs/acme/inventory/groups/g1/members/h1/accept': (req: MockRequest) => {
          accepted = req;
          return { member: { ...MEMBERSHIP, assistant_keys: [], revision: 5 } };
        },
      }));
      renderCard(true, true);
      await user.click(await screen.findByRole('button', { name: 'Edit values in group webserver' }));
      const dialog = await screen.findByRole('dialog');
      const mark = await within(dialog).findByTestId('assistant-member-domain');
      expect(mark).toHaveTextContent('domain: Written by an assistant — used as plain text until a person changes or accepts it');
      await user.click(within(mark).getByRole('button', { name: 'Accept domain as live' }));
      await screen.findByText('domain is live now');
      expect(accepted?.headers.get('If-Match')).toBe('"4"');
      expect(accepted?.body).toEqual({ keys: ['domain'] });
    });

    it('asks before removing the host from a group and then unassigns it', async () => {
      const user = userEvent.setup();
      let deleted = false;
      mockApi(routes({
        'DELETE /api/v1/orgs/acme/inventory/groups/g1/members/h1': () => {
          deleted = true;
          return new Response(null, { status: 204 });
        },
      }));
      renderCard(true, true);
      await user.click(await screen.findByRole('button', { name: 'Remove from group webserver…' }));
      const message = await acceptConfirm(user);
      expect(message).toContain('web-01');
      expect(message).toContain('webserver');
      await screen.findByText('web-01 removed from webserver');
      expect(deleted).toBe(true);
    });

    it('reads the host’s groups again after a change, so the list follows it', async () => {
      const user = userEvent.setup();
      let member = true;
      let reads = 0;
      mockApi(routes({
        'GET /api/v1/orgs/acme/hosts/h1/inventory/groups': () => {
          reads++;
          return { groups: member ? [MEMBERSHIP] : [] };
        },
        'DELETE /api/v1/orgs/acme/inventory/groups/g1/members/h1': () => {
          member = false;
          return new Response(null, { status: 204 });
        },
      }));
      renderCard(true, true);
      await user.click(await screen.findByRole('button', { name: 'Remove from group webserver…' }));
      await acceptConfirm(user);
      expect(await screen.findByText('Not in any inventory group yet.')).toBeInTheDocument();
      expect(screen.queryByRole('link', { name: 'webserver' })).not.toBeInTheDocument();
      expect(reads).toBe(2);
    });

    it('keeps the direct groups when the groups list for the inherited ones cannot be read', async () => {
      mockApi(routes({
        'GET /api/v1/orgs/acme/inventory/groups': () => new Response(JSON.stringify({ detail: 'boom' }), { status: 500, headers: { 'Content-Type': 'application/json' } }),
      }));
      renderCard(true, true);
      expect(await screen.findByRole('link', { name: 'webserver' })).toBeInTheDocument();
      expect(screen.getByRole('button', { name: 'Add to group…' })).toBeInTheDocument();
      expect(screen.queryByText('prod')).not.toBeInTheDocument();
    });

    it('says so when the group of a membership cannot be read for editing', async () => {
      const user = userEvent.setup();
      mockApi(routes({
        'GET /api/v1/orgs/acme/inventory/groups/g1': () => new Response(JSON.stringify({ detail: 'group read failed' }), { status: 500, headers: { 'Content-Type': 'application/json' } }),
      }));
      renderCard(true, true);
      await user.click(await screen.findByRole('button', { name: 'Edit values in group webserver' }));
      const dialog = await screen.findByRole('dialog', { name: 'Values of web-01' });
      await waitFor(() => expect(within(dialog).getByText('Something went wrong')).toBeInTheDocument());
    });

    it('keeps the membership when the confirmation is declined', async () => {
      const user = userEvent.setup();
      let deleted = false;
      mockApi(routes({ 'DELETE /api/v1/orgs/acme/inventory/groups/g1/members/h1': () => ((deleted = true), new Response(null, { status: 204 })) }));
      renderCard(true, true);
      await user.click(await screen.findByRole('button', { name: 'Remove from group webserver…' }));
      const dialog = await findConfirmDialog();
      await user.click(within(dialog).getAllByRole('button')[0]);
      expect(deleted).toBe(false);
    });

    it('shows the missing values of a membership on its row', async () => {
      mockApi(routes({
        'GET /api/v1/orgs/acme/hosts/h1/inventory/groups': { groups: [{ ...MEMBERSHIP, complete: false, problems: [{ path: 'domain', message: 'is required' }] }] },
      }));
      renderCard(true, true);
      expect(await screen.findByText('1 to fill in')).toBeInTheDocument();
    });
  });
});
