import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import userEvent from '@testing-library/user-event';
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
    items: [
      { to: '/o/$org/settings/mail', params: { org: 'acme' }, label: 'Outgoing mail' },
      { to: '/o/$org/settings/shared/repos', params: { org: 'acme' }, label: 'Shared repositories' },
    ],
  },
];

function renderNav(path = '/o/acme/settings/members') {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const leaf = (path: string) =>
    createRoute({ getParentRoute: () => orgRoute, path, component: () => <SideNav groups={GROUPS} label="Settings" /> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([
      orgRoute.addChildren([leaf('/settings/general'), leaf('/settings/members'), leaf('/settings/mail'), leaf('/settings/shared/repos'), leaf('/settings/shared/repos/$id')]),
    ]),
    history: createMemoryHistory({ initialEntries: [path] }),
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
    expect(await screen.findByRole('combobox', { name: 'Settings' })).toHaveValue('Members');
    expect(screen.queryByRole('link', { name: 'General' })).not.toBeInTheDocument();
    const user = userEvent.setup();
    await user.click(screen.getByRole('combobox', { name: 'Settings' }));
    await user.click(screen.getByRole('option', { name: /Outgoing mail/ }));
    await waitFor(() => expect(router.state.location.pathname).toBe('/o/acme/settings/mail'));
  });

  it('keeps the navigation landmark below 900px', async () => {
    mockNarrow(true);
    renderNav();
    const nav = await screen.findByRole('navigation', { name: 'Settings' });
    expect(within(nav).getByRole('combobox', { name: 'Settings' })).toBeInTheDocument();
  });

  it('shows the item whose path is the longest prefix of a nested page', async () => {
    mockNarrow(true);
    renderNav('/o/acme/settings/shared/repos/123');
    expect(await screen.findByRole('combobox', { name: 'Settings' })).toHaveValue('Shared repositories');
  });

  it('marks the item of a nested page in the wide list', async () => {
    mockNarrow(false);
    renderNav('/o/acme/settings/shared/repos/123');
    expect(await screen.findByRole('link', { name: 'Shared repositories' })).toHaveAttribute('aria-current', 'page');
  });
});
