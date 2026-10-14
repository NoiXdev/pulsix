import { describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
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
function renderShellAt(initialPath: string, opts: { onLogout?: () => void; role?: string } = {}) {
  const rootRoute = createRootRoute();

  function leaf(path: string) {
    return createRoute({
      getParentRoute: () => rootRoute,
      path,
      component: () => (
        <AppShell
          org={{ slug: initialPath.split('/')[2], name: 'Current Org', role: opts.role ?? 'owner' }}
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
    leaf('/o/$org/fleet'),
    leaf('/o/$org/groups'),
    leaf('/o/$org/checks'),
    leaf('/o/$org/checks/monitoring-groups'),
    leaf('/o/$org/credentials'),
    leaf('/o/$org/jump-hosts'),
    leaf('/o/$org/settings/members'),
    leaf('/o/$org/settings/activity'),
    leaf('/o/$org/settings/tokens'),
  ]);

  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [initialPath] }) });
  return render(<RouterProvider router={router} />);
}

describe('AppShell', () => {
  // Final review minor 7: the Workers page needs workers.read (admin and
  // above); the entry must not lead viewers and operators to a no-access
  // page.
  it.each([
    ['owner', true],
    ['admin', true],
    ['operator', false],
    ['viewer', false],
  ])('shows the Workers entry to a %s: %s', async (role, shown) => {
    renderShellAt('/o/acme/hosts', { role });
    await screen.findByText('page content');
    expect(screen.queryByRole('link', { name: 'Workers' }) !== null).toBe(shown);
  });

  // The Checks entry is gated on checks.read, which every named role has
  // — so the row that does any work is the last one: an unrecognised role
  // (a session whose org response has not landed, or a role this build
  // does not know) must not be offered a page it cannot open. Without
  // that row the gate could be deleted outright and this file would stay
  // green, which is what it did.
  it.each([
    ['owner', true],
    ['admin', true],
    ['operator', true],
    ['viewer', true],
    ['', false],
  ])('shows the Checks entry to a %s: %s', async (role, shown) => {
    renderShellAt('/o/acme/hosts', { role });
    await screen.findByText('page content');
    expect(screen.queryByRole('link', { name: 'Checks' }) !== null).toBe(shown);
  });

  // Same gate as Checks (checks.read, every named role) — an unrecognised
  // role must not be offered a page it cannot open, the same reasoning the
  // Checks case above pins.
  it.each([
    ['owner', true],
    ['admin', true],
    ['operator', true],
    ['viewer', true],
    ['', false],
  ])('shows the Fleet entry to a %s: %s', async (role, shown) => {
    renderShellAt('/o/acme/hosts', { role });
    await screen.findByText('page content');
    expect(screen.queryByRole('link', { name: 'Fleet' }) !== null).toBe(shown);
  });

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

    const groupsLink = screen.getByRole('link', { name: /Groups/ });
    expect(groupsLink).not.toHaveAttribute('data-nav-active');
  });

  it('marks Groups active on /groups', async () => {
    renderShellAt('/o/acme/groups');

    const groupsLink = await screen.findByRole('link', { name: /Groups/ });
    expect(groupsLink).toHaveAttribute('data-nav-active', 'true');
    expect(screen.getByRole('link', { name: /Hosts/ })).not.toHaveAttribute('data-nav-active');
  });

  // Monitoring groups live under /checks rather than as a second sidebar
  // entry (router.tsx's checksRoute), so the one Checks entry has to mark
  // itself on both pages of the section.
  it('marks Checks active on the definitions page and on the nested monitoring groups page', async () => {
    for (const path of ['/o/acme/checks', '/o/acme/checks/monitoring-groups']) {
      const { unmount } = renderShellAt(path);
      expect(await screen.findByRole('link', { name: /Checks/ })).toHaveAttribute('data-nav-active', 'true');
      expect(screen.getByRole('link', { name: /Hosts/ })).not.toHaveAttribute('data-nav-active');
      unmount();
    }
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

/**
 * The sidebar groups its fourteen entries under three headings rather
 * than listing them flat (v0.4f): Infrastructure, Monitoring,
 * Automation, with Settings left as a single ungrouped entry at the
 * bottom. The headings are separators, not disclosure controls —
 * nothing collapses, so no entry ever costs a second click.
 */
describe('AppShell nav grouping', () => {
  it.each(['Infrastructure', 'Monitoring', 'Automation'])('groups its entries under %s', async (label) => {
    renderShellAt('/o/acme/hosts');
    await screen.findByText('page content');
    expect(screen.getByRole('group', { name: label })).toBeInTheDocument();
  });

  it('puts each entry in the group it belongs to', async () => {
    renderShellAt('/o/acme/hosts');
    await screen.findByText('page content');
    const inGroup = (label: string, link: string) =>
      within(screen.getByRole('group', { name: label })).getByRole('link', { name: link });

    expect(inGroup('Infrastructure', 'Hosts')).toBeInTheDocument();
    expect(inGroup('Infrastructure', 'Jump hosts')).toBeInTheDocument();
    expect(inGroup('Infrastructure', 'Credentials')).toBeInTheDocument();
    expect(inGroup('Monitoring', 'Maintenance')).toBeInTheDocument();
    expect(inGroup('Automation', 'Runs')).toBeInTheDocument();
  });

  // Credentials and Jump hosts left the Settings tab strip for the
  // Infrastructure group, and their URLs moved with them — a page that no
  // longer appears in that strip must not keep living underneath it.
  it('links Credentials and Jump hosts at their own top-level paths', async () => {
    renderShellAt('/o/acme/hosts');
    await screen.findByText('page content');
    expect(screen.getByRole('link', { name: 'Credentials' })).toHaveAttribute('href', '/o/acme/credentials');
    expect(screen.getByRole('link', { name: 'Jump hosts' })).toHaveAttribute('href', '/o/acme/jump-hosts');
  });

  it('leaves Settings ungrouped', async () => {
    renderShellAt('/o/acme/hosts');
    await screen.findByText('page content');
    expect(screen.queryByRole('group', { name: 'Settings' })).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: /Settings/ })).toBeInTheDocument();
  });
});
