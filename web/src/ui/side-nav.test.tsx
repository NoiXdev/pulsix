import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { pickOption } from '../test/pickOption.ts';
import { SideNav, type SideNavGroup } from './side-nav.tsx';

const GROUPS: SideNavGroup[] = [
  {
    label: 'Organization',
    items: [
      { to: '/o/$org/settings/general', params: { org: 'acme' }, label: 'General' },
      { to: '/o/$org/settings/members', params: { org: 'acme' }, label: 'Members', badge: 4 },
    ],
  },
  {
    label: 'Instance',
    items: [{ to: '/o/$org/settings/mail', params: { org: 'acme' }, label: 'Outgoing mail' }],
  },
];

function renderNav() {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const leaf = (path: string) =>
    createRoute({ getParentRoute: () => orgRoute, path, component: () => <SideNav groups={GROUPS} label="Settings" /> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([
      orgRoute.addChildren([leaf('/settings/general'), leaf('/settings/members'), leaf('/settings/mail')]),
    ]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/settings/members'] }),
  });
  render(
          <RouterProvider router={router} />,
  );
  return router;
}

function mockNarrow(narrow: boolean) {
  vi.spyOn(window, 'matchMedia').mockImplementation(
    (query: string) =>
      ({
        matches: narrow,
        media: query,
        addEventListener: () => {},
        removeEventListener: () => {},
      }) as unknown as MediaQueryList,
  );
}

afterEach(() => vi.restoreAllMocks());

describe('SideNav', () => {
  it('lists the groups with their links and marks the current page', async () => {
    mockNarrow(false);
    renderNav();
    expect(await screen.findByText('Organization')).toBeInTheDocument();
    expect(screen.getByText('Instance')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'General' })).toHaveAttribute('href', '/o/acme/settings/general');
    expect(screen.getByRole('link', { name: /Members/ })).toHaveAttribute('aria-current', 'page');
    expect(screen.getByRole('link', { name: 'General' })).not.toHaveAttribute('aria-current');
    expect(screen.getByText('4')).toBeInTheDocument();
  });

  it('collapses into a select below 900px, and choosing an item navigates', async () => {
    mockNarrow(true);
    const router = renderNav();
    expect(await screen.findByLabelText('Settings')).toHaveValue('Members');
    expect(screen.queryByRole('link', { name: 'General' })).not.toBeInTheDocument();
    await pickOption(screen, 'Settings', 'Outgoing mail');
    await waitFor(() => expect(router.state.location.pathname).toBe('/o/acme/settings/mail'));
  });
});
