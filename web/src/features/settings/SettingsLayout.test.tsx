import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { SettingsLayout } from './SettingsLayout.tsx';

function renderLayout(path = '/o/acme/settings/members') {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const settingsRoute = createRoute({ getParentRoute: () => orgRoute, path: '/settings', component: SettingsLayout });
  const members = createRoute({ getParentRoute: () => settingsRoute, path: '/members', component: () => <div>page members</div> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([settingsRoute.addChildren([members])])]),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

describe('SettingsLayout', () => {
  it('renders only the page: the navigation lives in the shell', async () => {
    renderLayout();
    expect(await screen.findByText('page members')).toBeInTheDocument();
    expect(screen.queryByRole('navigation')).not.toBeInTheDocument();
    expect(screen.queryByRole('link')).not.toBeInTheDocument();
  });
});
