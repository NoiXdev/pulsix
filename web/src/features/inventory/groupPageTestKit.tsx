import { render } from '@testing-library/react';
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

/* The group page's test harness, shared by GroupDetailPage.layout.test.tsx
 * and GroupDetailPage.drafts.test.tsx: one group `web` (g1) below `prod`,
 * with a child group, two members and a jump host, and a router that
 * redirects old `?tab=` links the way the app's does. */

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
