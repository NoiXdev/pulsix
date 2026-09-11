import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {
  createRootRoute,
  createRoute,
  createRouter,
  createMemoryHistory,
  RouterProvider,
} from '@tanstack/react-router';
import { AppShell, type ShellOrg } from './shell.tsx';

const ORGS: ShellOrg[] = [
  { slug: 'acme', name: 'Acme Hosting' },
  { slug: 'hosts-inc', name: 'Hosts Inc' },
];

/** Builds a tiny standalone router just for this test, with the same
 * shape as the real one (a /hosts leaf and a /settings layout with
 * /members, /activity, /tokens leaves) — every leaf renders AppShell
 * itself so the "active route" assertions reflect real client-side
 * routing (finding "Important 11": the router's own activeProps, not a
 * hand-set `active` prop the shell used to take). */
function renderShellAt(initialPath: string, opts: { onLogout?: () => void } = {}) {
  const rootRoute = createRootRoute();

  function leaf(path: string) {
    return createRoute({
      getParentRoute: () => rootRoute,
      path,
      component: () => (
        <AppShell
          org={{ slug: initialPath.split('/')[2], name: 'Current Org' }}
          orgs={ORGS}
          onSelectOrg={() => {}}
          crumbs={<span>Hosts</span>}
          user={{ displayName: 'Tim R.', subtitle: 'Owner' }}
          onLogout={opts.onLogout ?? (() => {})}
        >
          <div>page content</div>
        </AppShell>
      ),
    });
  }

  const routeTree = rootRoute.addChildren([
    leaf('/o/$org/hosts'),
    leaf('/o/$org/settings/members'),
    leaf('/o/$org/settings/activity'),
    leaf('/o/$org/settings/tokens'),
  ]);

  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [initialPath] }) });
  return render(<RouterProvider router={router} />);
}

describe('AppShell', () => {
  it('shows the org switcher with the current org selected', async () => {
    renderShellAt('/o/acme/hosts');
    const select = (await screen.findByRole('combobox', { name: 'Organization' })) as HTMLSelectElement;
    expect(select.value).toBe('acme');
    expect(screen.getByRole('option', { name: 'Acme Hosting' })).toBeInTheDocument();
    expect(screen.getByRole('option', { name: 'Hosts Inc' })).toBeInTheDocument();
  });

  it('marks Hosts active — via the router\'s own activeProps — and leaves Settings unmarked', async () => {
    renderShellAt('/o/acme/hosts');

    const hostsLink = await screen.findByRole('link', { name: /Hosts/ });
    expect(hostsLink).toHaveAttribute('data-nav-active', 'true');
    expect(hostsLink).toHaveAttribute('aria-current', 'page');

    const settingsLink = screen.getByRole('link', { name: /Settings/ });
    expect(settingsLink).not.toHaveAttribute('data-nav-active');
  });

  it('marks Settings active for every route nested under /settings (Members, Activity, Tokens)', async () => {
    for (const path of ['/o/acme/settings/members', '/o/acme/settings/activity', '/o/acme/settings/tokens']) {
      const { unmount } = renderShellAt(path);
      expect(await screen.findByRole('link', { name: /Settings/ })).toHaveAttribute('data-nav-active', 'true');
      expect(screen.getByRole('link', { name: /Hosts/ })).not.toHaveAttribute('data-nav-active');
      unmount();
    }
  });

  it('does not mark Hosts active on a members route under an org slug containing "hosts" (regression for the pathname.includes bug)', async () => {
    // finding "Important 11": the old derivation used
    // `pathname.includes('/hosts')`, which matches
    // "/o/hosts-inc/settings/members" because the *org slug* itself
    // contains "hosts" — nothing to do with the Hosts page at all. The
    // router's own route-based matching must not repeat that mistake.
    renderShellAt('/o/hosts-inc/settings/members');

    expect(await screen.findByRole('link', { name: /Settings/ })).toHaveAttribute('data-nav-active', 'true');
    expect(screen.getByRole('link', { name: /Hosts/ })).not.toHaveAttribute('data-nav-active');
  });

  it('the footer menu links to account security and can log out', async () => {
    const user = userEvent.setup();
    const onLogout = vi.fn();
    renderShellAt('/o/acme/hosts', { onLogout });

    await user.click(await screen.findByRole('button', { name: 'Account menu' }));
    expect(screen.getByRole('link', { name: 'Account security' })).toHaveAttribute('href', '/account/security');

    await user.click(screen.getByRole('button', { name: 'Log out' }));
    expect(onLogout).toHaveBeenCalledOnce();
  });
});
