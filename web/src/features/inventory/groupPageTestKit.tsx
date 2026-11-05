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
  redirect,
  RouterProvider,
} from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { GroupDetailPage } from './GroupDetailPage.tsx';

/* The group page's test harness, shared by the GroupDetailPage.*.test.tsx
 * files: one group `web` (g1) below `prod`, with a child group, two members
 * and a jump host, a router that redirects old `?tab=` links the way the
 * app's does, and the steps the draft and revision tests share. */

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

/** The section an old `?tab=` link names, as the app router maps it. */
const TAB_SECTION: Record<string, string> = { members: 'members', values: 'values', schema: 'schema', children: 'children', preview: 'preview' };

export function renderAt(path: string, { browser = false } = {}) {
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

/** The number of fields in the schema editor (its rows past the header;
 * no field is expanded in these tests, so one row is one field). */
export const fieldCount = () => within(screen.getByRole('table', { name: 'Fields' })).getAllByRole('row').length - 1;

/** addField adds a new field to the schema draft through its dialog. */
export async function addField(user: User) {
  await user.click(screen.getByRole('button', { name: 'Add field' }));
  const dialog = await screen.findByRole('dialog', { name: /^Field / });
  await user.click(within(dialog).getByRole('button', { name: 'Done' }));
  await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
}

/** refetch refetches every query and lets the page render the result
 * (the query client hands results to the page on a later tick). */
export async function refetch(qc: QueryClient) {
  await act(async () => {
    await qc.refetchQueries();
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}
