import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { ChecksPage } from './ChecksPage.tsx';

/** Shared fixtures for the Checks page's tests: the API shapes, a router with
 * the page next to stub targets, and the row-menu helper. */

export const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });

export const check = (over: Record<string, unknown> = {}) => ({
  id: 'c1', key: 'cpu', plugin: 'cpu', params: {}, interval_seconds: 60, enabled: true,
  origin: 'seed', binding_kind: 'org', created_at: '', updated_at: '', ...over,
});

export const GROUPS = { groups: [{ id: 'g1', name: 'Web tier', selector: 'role=web', priority: 10, created_at: '', updated_at: '' }] };

export function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const checksRoute = createRoute({ getParentRoute: () => orgRoute, path: '/checks' });
  const indexRoute = createRoute({ getParentRoute: () => checksRoute, path: '/', component: ChecksPage });
  const groupsRoute = createRoute({ getParentRoute: () => checksRoute, path: '/monitoring-groups', component: () => <div>GROUPS VIEW</div> });
  const hostRoute = createRoute({ getParentRoute: () => orgRoute, path: '/hosts/$id', component: () => <div>HOST VIEW</div> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([checksRoute.addChildren([indexRoute, groupsRoute]), hostRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/checks'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

export async function chooseFromMenu(user: ReturnType<typeof userEvent.setup>, name: string, item: string) {
  await user.click(await screen.findByRole('button', { name: `Actions for ${name}` }));
  await user.click(await screen.findByRole('menuitem', { name: item }));
}
