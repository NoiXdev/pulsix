import { expect } from 'vitest';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import type userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  createBrowserHistory,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { GroupDetailPage } from './GroupDetailPage.tsx';
import { formerGroupLink, validateGroupSearch } from './groupTabs.ts';

/* The group page's test harness, shared by the GroupDetailPage.*.test.tsx
 * files: one group `web` (g1) below `prod`, with a child group, two members
 * and a jump host, a router that reads `?tab=` and sends old links
 * on with the app's own beforeLoad, and the steps the draft and revision
 * tests share. */

export const SCHEMA = {
  fields: [
    { name: 'env', type: 'string', required: true },
    { name: 'domain', type: 'string', scope: 'host', required: true },
  ],
};

export const group = (extra = {}) => ({
  group: {
    id: 'g1', name: 'web', description: 'Front web servers', parent_id: 'p1', bastion_id: 'b1', revision: 3, schema: SCHEMA,
    vars: { env: 'prod' }, problems: [], orphaned: [], created_at: '', updated_at: '', ...extra,
  },
});

/** A group with a stored secret, which every read returns masked: a refetch
 * of it brings the same values whatever its revision, unless `env` changed. */
export const withSecret = (revision: number, env = 'prod') =>
  group({
    revision,
    schema: { fields: [...SCHEMA.fields, { name: 'db_password', type: 'secret' }] },
    vars: { env, db_password: { $secret: 'set' } },
  });

export const summary = (id: string, name: string, parent_id: string | null, extra = {}) => ({
  id, name, description: '', parent_id, members: 0, group_fields: 1, host_fields: 0, complete: true, revision: 1, ...extra,
});

export const GROUPS = {
  groups: [
    summary('p1', 'prod', null),
    summary('p2', 'staging', null),
    summary('g1', 'web', 'p1', { members: 2, group_fields: 1, host_fields: 1, complete: false, bastion_id: 'b1', revision: 3 }),
    summary('c1', 'web_eu', 'g1', { members: 4 }),
  ],
};

export const member = (host_id: string, host_name: string, extra = {}) => ({
  group_id: 'g1', group_name: 'web', host_id, host_name, vars: { domain: `${host_name}.acme.io` }, complete: true, problems: [], orphaned: [], revision: 7, ...extra,
});

export const MEMBERS = {
  members: [member('h1', 'web-01'), member('h2', 'web-02', { vars: {}, complete: false, problems: [{ path: 'domain', message: 'is required' }] })],
};

const org = (role: string) => ({ org: { id: 'o1', name: 'Acme', slug: 'acme', role, created_at: '2026-01-01T00:00:00Z' } });

export const routes = (role = 'admin', extra: Record<string, unknown> = {}) => ({
  'GET /api/v1/orgs/acme': org(role),
  'GET /api/v1/orgs/acme/inventory/groups/g1': group(),
  'GET /api/v1/orgs/acme/inventory/groups': GROUPS,
  'GET /api/v1/orgs/acme/inventory/groups/g1/members': MEMBERS,
  'GET /api/v1/orgs/acme/inventory/preview': { inventory: { all: { children: ['web'] } }, connections: {}, warnings: [] },
  'GET /api/v1/orgs/acme/bastions': { bastions: [{ id: 'b1', name: 'bastion-fra', address: 'b.example', port: 22, username: 'ops' }] },
  'GET /api/v1/orgs/acme/credentials': { credentials: [] },
  ...extra,
});

export function renderAt(path: string, { browser = false } = {}) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const detail = createRoute({
    getParentRoute: () => orgRoute,
    path: '/groups/$id',
    validateSearch: validateGroupSearch,
    beforeLoad: formerGroupLink,
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
        <ConfirmProvider>
          <RouterProvider router={router} />
        </ConfirmProvider>
      </ToastProvider>
    </QueryClientProvider>,
  );
  return { ...view, router, qc };
}

type User = ReturnType<typeof userEvent.setup>;

/** stale is the API's answer to a write on a revision that is no longer
 * current. */
export const stale = () => new Response(JSON.stringify({ status: 412, detail: 'changed' }), { status: 412, headers: { 'Content-Type': 'application/json' } });

/** schemaSaved is the answer to a schema save that stored `schema` as
 * `revision`. */
export const schemaSaved = (revision: number, schema: unknown) => ({ ...group({ revision, schema }), incomplete_members: 0 });

/** typeEnv replaces the group value `env` with `text`. */
export async function typeEnv(user: User, text: string) {
  const env = await screen.findByLabelText('env');
  await user.clear(env);
  await user.type(env, text);
}

/** valuesCard is the Variables tab's card of group values. */
export const valuesCard = () => screen.getByRole('region', { name: 'Group values' });

const FIELDS_TITLE = /^(Manage fields|Fields)$/;

/** fieldsDialog is the open Manage fields dialog (titled Fields for a
 * reader). */
export const fieldsDialog = () => screen.getByRole('dialog', { name: FIELDS_TITLE });

/** openFields opens the Manage fields dialog, which holds the schema
 * editor, and returns it. */
export async function openFields(user: User) {
  await user.click(await screen.findByRole('button', { name: /^(Manage fields|Fields) …$/ }));
  return screen.findByRole('dialog', { name: FIELDS_TITLE });
}

/** closeFields closes the Manage fields dialog when it is open (a save
 * closes it by itself); an edited draft is discarded on the way. */
export async function closeFields(user: User) {
  const dialog = screen.queryByRole('dialog', { name: FIELDS_TITLE });
  if (!dialog) return;
  await user.click(within(dialog).getByRole('button', { name: /^(Cancel|Close)$/ }));
  const confirm = screen.queryByRole('dialog', { name: 'Discard changes?' });
  if (confirm) await user.click(within(confirm).getByRole('button', { name: 'Discard' }));
  await waitFor(() => expect(screen.queryByRole('dialog', { name: FIELDS_TITLE })).not.toBeInTheDocument());
}

/** saveFields saves the schema draft from the dialog's footer. */
export async function saveFields(user: User) {
  const save = () => within(fieldsDialog()).getByRole('button', { name: 'Save' });
  await waitFor(() => expect(save()).toBeEnabled());
  await user.click(save());
}

/** openEdit opens the Edit dialog from the header's ⋯ menu. */
export async function openEdit(user: User, name = 'web') {
  await user.click(await screen.findByRole('button', { name: `More actions for ${name}` }));
  await user.click(screen.getByRole('menuitem', { name: 'Edit …' }));
  return screen.findByRole('dialog', { name: 'Edit group' });
}

/** openTab opens one of the group page's tabs. */
export async function openTab(user: User, name: RegExp | string) {
  await user.click(await screen.findByRole('tab', { name }));
}

/** The number of fields in the schema editor (one row per field). */
export const fieldCount = () => within(within(fieldsDialog()).getByRole('list', { name: 'Fields' })).getAllByRole('listitem').length;

/** addField adds a new field to the schema draft. */
export async function addField(user: User) {
  const before = fieldCount();
  await user.click(within(fieldsDialog()).getByRole('button', { name: 'Add field' }));
  await waitFor(() => expect(fieldCount()).toBe(before + 1));
}

/** refetch refetches every query and lets the page render the result
 * (the query client hands results to the page on a later tick). */
export async function refetch(qc: QueryClient) {
  await act(async () => {
    await qc.refetchQueries();
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}
