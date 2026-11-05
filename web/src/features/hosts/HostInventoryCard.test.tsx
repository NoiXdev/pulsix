import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { mockApi } from '../../test/mockApi.ts';
import { HostInventoryCard } from './HostInventoryCard.tsx';

function renderCard(managed: boolean) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute({ component: () => <HostInventoryCard org="acme" hostId="h1" managed={managed} /> });
  const groupRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/o/$org/groups/$id',
    validateSearch: (s: Record<string, unknown>) => ({ tab: typeof s.tab === 'string' ? s.tab : undefined }),
  });
  const router = createRouter({ routeTree: rootRoute.addChildren([groupRoute]), history: createMemoryHistory({ initialEntries: ['/'] }) });
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

describe('HostInventoryCard', () => {
  it('explains that inventory groups need the Managed capability', async () => {
    mockApi({});
    renderCard(false);
    expect(await screen.findByText('Inventory groups need the Managed capability.')).toBeInTheDocument();
  });

  it('says when a managed host is in no group', async () => {
    mockApi({ 'GET /api/v1/orgs/acme/hosts/h1/inventory/groups': { groups: [] } });
    renderCard(true);
    expect(await screen.findByText('Not in any inventory group yet.')).toBeInTheDocument();
  });

  it('links each group to its members section and flags missing values', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/inventory/groups': {
        groups: [{ group_id: 'g1', group_name: 'webserver', host_id: 'h1', host_name: 'web-01', vars: {}, complete: false, problems: [{ path: 'domain', message: 'is required' }], orphaned: [], revision: 1 }],
      },
    });
    renderCard(true);
    const link = await screen.findByRole('link', { name: 'webserver' });
    expect(link.getAttribute('href')).toBe('/o/acme/groups/g1#members');
    expect(screen.getByText('1 to fill in')).toBeInTheDocument();
  });
});
