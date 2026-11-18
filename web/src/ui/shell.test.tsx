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
import { AppShell, type ShellOrg, type ShellSettingsNav } from './shell.tsx';

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
function renderShellAt(initialPath: string, opts: { onLogout?: () => void; role?: string; onCreateOrg?: () => void; onLockSession?: () => void; version?: string; pendingRunRequests?: number; settingsNav?: ShellSettingsNav } = {}) {
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
          onCreateOrg={opts.onCreateOrg}
          onLockSession={opts.onLockSession}
          version={opts.version}
          pendingRunRequests={opts.pendingRunRequests}
          settingsNav={opts.settingsNav}
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
    leaf('/o/$org/settings/members/$userId'),
    leaf('/o/$org/settings/general'),
    leaf('/o/$org/settings/activity'),
    leaf('/o/$org/settings/tokens'),
    leaf('/account/security'),
    leaf('/account/appearance'),
    leaf('/account/profile'),
    leaf('/account/activity'),
  ]);

  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [initialPath] }) });
  return render(<RouterProvider router={router} />);
}

describe('AppShell', () => {
  it('has a lock button in the top bar that calls onLockSession', async () => {
    const onLockSession = vi.fn();
    renderShellAt('/o/acme/hosts', { onLockSession });
    await userEvent.click(await screen.findByRole('button', { name: 'Lock session' }));
    expect(onLockSession).toHaveBeenCalledTimes(1);
  });

  it('shows no lock button when the page offers no way to lock', async () => {
    renderShellAt('/o/acme/hosts');
    await screen.findByText('page content');
    expect(screen.queryByRole('button', { name: 'Lock session' })).not.toBeInTheDocument();
  });

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
    const user = userEvent.setup();
    renderShellAt('/o/acme/hosts');
    const combo = await screen.findByRole('combobox', { name: 'Organization' });
    // The displayed value is the matching ORGS entry's label, not
    // org.name — the fixture's "Current Org" is deliberately a different
    // string, the same way the native select ignored org.name too (it
    // only ever rendered <option> text from the orgs list).
    expect(combo).toHaveValue('Acme Hosting');
    await user.click(combo);
    expect(screen.getByRole('option', { name: 'Acme Hosting' })).toBeInTheDocument();
    expect(screen.getByRole('option', { name: 'Hosts Inc' })).toBeInTheDocument();
  });

  it('offers no create entry in the org switcher without onCreateOrg', async () => {
    const user = userEvent.setup();
    renderShellAt('/o/acme/hosts');
    await user.click(await screen.findByRole('combobox', { name: 'Organization' }));
    expect(screen.getAllByRole('option').map((o) => o.getAttribute('data-label'))).toEqual(['Acme Hosting', 'Hosts Inc']);
  });

  it('ends the org switcher with "+ New organization" when onCreateOrg is set, and calls it on choosing it', async () => {
    const user = userEvent.setup();
    const onCreateOrg = vi.fn();
    renderShellAt('/o/acme/hosts', { onCreateOrg });
    await user.click(await screen.findByRole('combobox', { name: 'Organization' }));

    const options = screen.getAllByRole('option');
    expect(options.map((o) => o.getAttribute('data-label'))).toEqual(['Acme Hosting', 'Hosts Inc', 'New organization']);
    await user.click(options[2]);

    expect(onCreateOrg).toHaveBeenCalledTimes(1);
  });

  it('marks Hosts active — via the router\'s own activeProps — and leaves Settings unmarked', async () => {
    renderShellAt('/o/acme/hosts');

    const hostsLink = await screen.findByRole('link', { name: /Hosts/ });
    expect(hostsLink).toHaveAttribute('data-nav-active', 'true');
    expect(hostsLink).toHaveAttribute('aria-current', 'page');

    const settingsLink = within(screen.getByRole('navigation')).getByRole('link', { name: /Settings/ });
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
      expect(within(await screen.findByRole('navigation')).getByRole('link', { name: /Settings/ })).toHaveAttribute('data-nav-active', 'true');
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

    expect(within(await screen.findByRole('navigation')).getByRole('link', { name: /Settings/ })).toHaveAttribute('data-nav-active', 'true');
    expect(screen.getByRole('link', { name: /Hosts/ })).not.toHaveAttribute('data-nav-active');
  });

  it('the footer menu links to the settings and can log out', async () => {
    const user = userEvent.setup();
    const onLogout = vi.fn();
    renderShellAt('/o/acme/hosts', { onLogout });

    await user.click(await screen.findByRole('button', { name: 'Account menu' }));
    // The org nav has a "Settings" entry too: scope to the account menu.
    const menu = screen.getByRole('button', { name: 'Account menu' }).closest('details') as HTMLElement;
    expect(within(menu).getByRole('link', { name: 'Settings' })).toHaveAttribute('href', '/account/security');
    expect(within(menu).queryByRole('link', { name: 'Account security' })).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Log out' }));
    expect(onLogout).toHaveBeenCalledOnce();
  });

  it('the footer menu opens the API explorer in a new tab', async () => {
    const user = userEvent.setup();
    renderShellAt('/o/acme/hosts');

    await user.click(await screen.findByRole('button', { name: 'Account menu' }));
    // The explorer is a page the server serves, not an SPA route.
    const link = screen.getByRole('link', { name: /API explorer/ });
    expect(link).toHaveAttribute('href', '/api/explorer');
    expect(link).toHaveAttribute('target', '_blank');
    expect(link).toHaveAttribute('rel', 'noopener noreferrer');
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
    expect(within(screen.getByRole('navigation')).getByRole('link', { name: /Settings/ })).toBeInTheDocument();
  });

  it.each([
    ['0.1.0', 'v0.1.0'],
    ['v0.1.0-rc.2', 'v0.1.0-rc.2'],
    ['dev', 'dev'],
  ])('shows the Pulsix version %s as %s beside the wordmark', async (version, shown) => {
    renderShellAt('/o/acme/hosts', { version });
    await screen.findByText('page content');
    expect(screen.getByTitle('Pulsix version')).toHaveTextContent(shown);
  });

  it('shows no version until it is known', async () => {
    renderShellAt('/o/acme/hosts');
    await screen.findByText('page content');
    expect(screen.queryByTitle('Pulsix version')).not.toBeInTheDocument();
  });

  // Run requests (spec 2026-11-16): the Runs entry carries the number of
  // requests waiting for a person, and nothing while none is waiting.
  it('shows the pending run requests beside the Runs entry', async () => {
    renderShellAt('/o/acme/hosts', { pendingRunRequests: 3 });
    await screen.findByText('page content');
    const runs = within(screen.getByRole('group', { name: 'Automation' })).getByRole('link', { name: /^Runs/ });
    expect(runs).toHaveTextContent('3');
    expect(within(runs).getByText('3 pending run requests')).toBeInTheDocument();
  });

  it.each([0, undefined])('shows no run request badge for %s pending', async (pending) => {
    renderShellAt('/o/acme/hosts', { pendingRunRequests: pending });
    await screen.findByText('page content');
    expect(screen.getByRole('link', { name: 'Runs' })).toBeInTheDocument();
    expect(screen.queryByText(/pending run requests/)).not.toBeInTheDocument();
  });

  describe('with the settings navigation', () => {
    const settingsNav: ShellSettingsNav = { backOrg: { slug: 'acme', name: 'Acme' }, personName: 'Tim R.', personRole: 'Owner', showAlerting: true, showInstance: true };

    it('replaces the organization switcher and navigation by the back link, the person and the pages', async () => {
      renderShellAt('/account/appearance', { settingsNav });

      const nav = await screen.findByRole('navigation', { name: 'Settings' });
      expect(within(nav).getByRole('link', { name: 'Back to Acme' })).toHaveAttribute('href', '/o/acme/hosts');
      expect(within(nav).getByText('Tim R.')).toBeInTheDocument();
      expect(within(nav).getByText('Owner')).toBeInTheDocument();
      expect(within(nav).getByRole('link', { name: 'Appearance' })).toHaveAttribute('data-nav-active', 'true');
      expect(within(nav).getByRole('link', { name: 'Security' })).not.toHaveAttribute('data-nav-active');
      expect(within(nav).getByRole('link', { name: 'Profile' })).toHaveAttribute('href', '/account/profile');
      expect(within(nav).getByRole('link', { name: 'My activity' })).toHaveAttribute('href', '/account/activity');
      expect(screen.queryByRole('combobox', { name: 'Organization' })).not.toBeInTheDocument();
      expect(screen.queryByRole('link', { name: /^Hosts/ })).not.toBeInTheDocument();
    });

    it('groups My account, Organization, Alerting and Instance, the organization links pointing at the back-org', async () => {
      renderShellAt('/o/acme/settings/members/u2', { settingsNav });
      const nav = await screen.findByRole('navigation', { name: 'Settings' });
      for (const name of ['My account', 'Organization · Acme', 'Alerting', 'Instance']) {
        expect(within(nav).getByRole('group', { name })).toBeInTheDocument();
      }
      const org = within(nav).getByRole('group', { name: 'Organization · Acme' });
      expect(within(org).getByRole('link', { name: 'General' })).toHaveAttribute('href', '/o/acme/settings/general');
      expect(within(org).getByRole('link', { name: 'Secrets' })).toHaveAttribute('href', '/o/acme/settings/secrets');
      expect(within(org).getByRole('link', { name: 'Members' })).toHaveAttribute('data-nav-active', 'true');
      const instance = within(nav).getByRole('group', { name: 'Instance' });
      expect(within(instance).getByRole('link', { name: 'Shared check definitions' })).toHaveAttribute('href', '/o/acme/settings/shared/checks');
      expect(within(nav).getByRole('group', { name: 'Alerting' })).toBeInTheDocument();
    });

    it('hides Alerting and Instance when the booleans are false', async () => {
      renderShellAt('/o/acme/settings/general', { settingsNav: { ...settingsNav, showAlerting: false, showInstance: false } });
      const nav = await screen.findByRole('navigation', { name: 'Settings' });
      expect(within(nav).getByRole('group', { name: 'Organization · Acme' })).toBeInTheDocument();
      expect(within(nav).queryByRole('group', { name: 'Alerting' })).not.toBeInTheDocument();
      expect(within(nav).queryByRole('group', { name: 'Instance' })).not.toBeInTheDocument();
    });

    it('keeps the account footer menu', async () => {
      renderShellAt('/account/security', { settingsNav });
      expect(await screen.findByRole('button', { name: 'Account menu' })).toBeInTheDocument();
    });
  });
});
