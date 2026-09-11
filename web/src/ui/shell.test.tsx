import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import {
  createRootRoute,
  createRoute,
  createRouter,
  createMemoryHistory,
  RouterProvider,
} from '@tanstack/react-router';
import { AppShell } from './shell.tsx';

/** Builds a tiny standalone router just for this test — a handful of leaf
 * routes matching the paths AppShell links to, each rendering AppShell
 * itself so the "active route" assertion reflects real client-side
 * routing rather than a hand-set prop. */
function renderShellAt(initialPath: string) {
  const rootRoute = createRootRoute();

  function leaf(path: string, active: 'hosts' | 'members' | 'activity' | 'tokens') {
    return createRoute({
      getParentRoute: () => rootRoute,
      path,
      component: () => (
        <AppShell
          org={{ slug: 'acme', name: 'Acme Hosting' }}
          active={active}
          crumbs={<span>Hosts</span>}
          user={{ displayName: 'Tim R.', subtitle: 'Owner · passkey' }}
        >
          <div>page content</div>
        </AppShell>
      ),
    });
  }

  const routeTree = rootRoute.addChildren([
    leaf('/o/$org/hosts', 'hosts'),
    leaf('/o/$org/members', 'members'),
    leaf('/o/$org/activity', 'activity'),
    leaf('/o/$org/settings/tokens', 'tokens'),
  ]);

  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [initialPath] }) });
  return render(<RouterProvider router={router} />);
}

describe('AppShell', () => {
  it('shows the org switcher with the current org name', async () => {
    renderShellAt('/o/acme/hosts');
    expect(await screen.findByText('Acme Hosting')).toBeInTheDocument();
  });

  it('marks the active route (Hosts) and leaves the others unmarked', async () => {
    renderShellAt('/o/acme/hosts');

    const hostsLink = await screen.findByRole('link', { name: /Hosts/ });
    expect(hostsLink).toHaveAttribute('aria-current', 'page');

    const membersLink = screen.getByRole('link', { name: /Members/ });
    expect(membersLink).not.toHaveAttribute('aria-current');
  });

  it('marks Members as active when that route is current instead', async () => {
    renderShellAt('/o/acme/members');

    expect(await screen.findByRole('link', { name: /Members/ })).toHaveAttribute('aria-current', 'page');
    expect(screen.getByRole('link', { name: /^Hosts$/ })).not.toHaveAttribute('aria-current');
  });
});
