import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/react-query';
import { mockApi, type MockRequest, type MockRoute } from '../../test/mockApi.ts';
import { agentBody, alertBody, check, HOST, hostRoutes, NOW, orgBody, reading, renderAt } from '../../test/hostDetail.tsx';
import { clippedElements } from '../../test/clipped.ts';
import { HostDetailPage } from './HostDetailPage.tsx';
import { acceptConfirm } from '../../test/confirmDialog.ts';
import { expandChecks } from '../../test/expandChecks.ts';

const renderPage = (qc?: QueryClient, path?: string) => renderAt(HostDetailPage, qc, path);
const renderTab = (tab: string) => renderAt(HostDetailPage, undefined, `/o/acme/hosts/h1?tab=${tab}`);

/** The tab strip, as label → count ('' for a tab without one). */
function tabItems(): Array<[string, string]> {
  const list = screen.getByRole('tablist', { name: 'Host sections' });
  return within(list)
    .getAllByRole('tab')
    .map((tab) => {
      const spans = tab.querySelectorAll('span');
      return [spans[0]?.textContent ?? '', spans[1]?.textContent ?? ''];
    });
}

const tab = (name: string) => screen.getByRole('tab', { name: new RegExp(`^${name}`) });
const selectedTab = () =>
  screen
    .getAllByRole('tab')
    .find((t) => t.getAttribute('aria-selected') === 'true')
    ?.querySelector('span')?.textContent;
const panel = () => within(screen.getByRole('tabpanel'));

const MONITORED_CHECKS = {
  host_id: 'h1',
  monitored: true,
  checks: [check('cpu', 'cpu', [reading('cpu.used_pct', 23)]), check('memory', 'memory', [reading('mem.used_pct', 61)])],
};

const monitoredRoutes = (extra: Record<string, MockRoute> = {}, role = 'admin') =>
  hostRoutes(
    { host: { managed: true, monitored: true }, role },
    {
      'GET /api/v1/orgs/acme/hosts/h1/checks': MONITORED_CHECKS,
      'GET /api/v1/orgs/acme/hosts/h1/metrics': { points: [] },
      ...extra,
    },
  );

describe('HostDetailPage', () => {
  it('shows the header and the tabs with their counts, on Overview by default', async () => {
    mockApi(monitoredRoutes({ 'GET /api/v1/orgs/acme/alerts': { alerts: [alertBody('a1', 'warn'), alertBody('a2', 'warn')], next_cursor: '' } }));
    const { router } = renderPage();

    expect(await screen.findByRole('heading', { level: 1, name: 'web-04.fra' })).toBeInTheDocument();
    const facts = within(screen.getByTestId('host-facts'));
    expect(facts.getByText('203.0.113.4')).toBeInTheDocument();
    expect(await facts.findByText('fra-1')).toBeInTheDocument();
    expect(facts.getByText('debian 13')).toBeInTheDocument();
    expect(await screen.findByText('2 warnings')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Run command' })).toBeInTheDocument();

    await waitFor(() =>
      expect(tabItems()).toEqual([
        ['Overview', ''],
        ['Checks', '2'],
        ['Alerts', '2'],
        ['Runs', ''],
        ['Inventory & facts', ''],
        ['Connection', ''],
      ]),
    );
    expect(selectedTab()).toBe('Overview');
    // Open alerts are highlighted; the checks count is not.
    expect(tab('Alerts').querySelector('[data-tone]')).toHaveAttribute('data-tone', 'warn');
    expect(tab('Checks').querySelector('[data-tone]')).toBeNull();
    expect(router.state.location.search).toEqual({});
  });

  it('switches tabs and keeps the open one in the URL', async () => {
    const user = userEvent.setup();
    mockApi(monitoredRoutes());
    const { router } = renderPage();

    await user.click(await screen.findByRole('tab', { name: /^Checks/ }));
    expect(selectedTab()).toBe('Checks');
    expect(router.state.location.search).toEqual({ tab: 'checks' });
    expect(panel().getByRole('link', { name: 'Manage checks' })).toBeInTheDocument();

    await user.click(tab('Inventory & facts'));
    expect(router.state.location.search).toEqual({ tab: 'inventory' });
    expect(panel().getByRole('region', { name: 'Facts' })).toBeInTheDocument();

    await user.click(tab('Overview'));
    expect(router.state.location.search).toEqual({});
    expect(selectedTab()).toBe('Overview');
  });

  it('moves between the tabs with the arrow keys', async () => {
    const user = userEvent.setup();
    mockApi(monitoredRoutes());
    const { router } = renderPage();

    const overview = await screen.findByRole('tab', { name: /^Overview/ });
    expect(overview).toHaveAttribute('tabindex', '0');
    expect(tab('Checks')).toHaveAttribute('tabindex', '-1');
    overview.focus();
    await user.keyboard('{ArrowRight}');
    expect(tab('Checks')).toHaveFocus();
    expect(router.state.location.search).toEqual({ tab: 'checks' });
    await user.keyboard('{End}');
    expect(tab('Connection')).toHaveFocus();
    await user.keyboard('{ArrowRight}');
    expect(tab('Overview')).toHaveFocus();
  });

  it('opens the tab the URL names', async () => {
    mockApi(monitoredRoutes());
    renderTab('connection');

    await screen.findByRole('tablist', { name: 'Host sections' });
    expect(selectedTab()).toBe('Connection');
    expect(await panel().findByRole('heading', { level: 3, name: 'SSH access' })).toBeInTheDocument();
    expect(panel().getByRole('heading', { level: 3, name: 'Agent' })).toBeInTheDocument();
    expect(panel().getByRole('heading', { level: 2, name: 'Connection & SSH' })).toBeInTheDocument();
  });

  it('falls back to Overview for a tab it does not know', async () => {
    mockApi(monitoredRoutes());
    renderTab('nope');

    await screen.findByRole('tablist', { name: 'Host sections' });
    expect(selectedTab()).toBe('Overview');
  });

  it('overview: key metrics, open alerts, the last runs, connection and checks at a glance', async () => {
    const user = userEvent.setup();
    let jobsAsked: Record<string, string> = {};
    mockApi(
      monitoredRoutes({
        'GET /api/v1/orgs/acme/hosts/h1/checks': {
          ...MONITORED_CHECKS,
          checks: [...MONITORED_CHECKS.checks, check('disk', 'disk', [reading('disk.used_pct', 91, { item: '/', state: 'warn' })])],
        },
        'GET /api/v1/orgs/acme/alerts': { alerts: [alertBody('a1', 'warn')], next_cursor: '' },
        'GET /api/v1/orgs/acme/jobs': (req: MockRequest) => {
          jobsAsked = Object.fromEntries(req.search.entries());
          return { jobs: [], next_cursor: '' };
        },
        'GET /api/v1/orgs/acme/hosts/h1/ssh': { ssh: { host_id: 'h1', username: 'ops', port: 2222, bastion_mode: 'inherit' } },
        'GET /api/v1/orgs/acme/hosts/h1/connection-test': {
          test: { at: NOW, ok: true, host_key: 'SHA256:x', host_key_state: 'pinned', checks: [] },
        },
      }),
    );
    renderPage();

    expect(await screen.findByText('23 %')).toBeInTheDocument();
    expect(await screen.findByTestId('alert-open')).toBeInTheDocument();
    await waitFor(() => expect(jobsAsked).toMatchObject({ host_id: 'h1', limit: '3' }));

    const connection = within(screen.getByRole('region', { name: 'Connection' }));
    expect(await connection.findByText('ops@203.0.113.4:2222')).toBeInTheDocument();
    expect(await connection.findByText('direct connection')).toBeInTheDocument();
    expect(await connection.findByText('ok')).toBeInTheDocument();

    const checks = within(screen.getByRole('region', { name: 'Checks' }));
    expect(checks.getByText('2 ok')).toBeInTheDocument();
    expect(checks.getByText('1 warning')).toBeInTheDocument();
    expect(checks.queryByText(/critical/)).not.toBeInTheDocument();

    await user.click(checks.getByRole('button', { name: 'All 3' }));
    expect(selectedTab()).toBe('Checks');
    await user.click(tab('Overview'));
    await user.click(await screen.findByRole('button', { name: 'All runs' }));
    expect(selectedTab()).toBe('Runs');
    expect(panel().getByRole('link', { name: 'All runs for this host' })).toBeInTheDocument();
    await user.click(tab('Overview'));
    await user.click(await within(screen.getByRole('region', { name: 'Connection' })).findByRole('button', { name: 'Change…' }));
    expect(await screen.findByRole('dialog', { name: 'SSH access · web-04.fra' })).toBeInTheDocument();
  });

  it('reads the organization once for the whole page', async () => {
    let orgGets = 0;
    mockApi(
      hostRoutes(
        { host: { managed: true } },
        {
          'GET /api/v1/orgs/acme': () => {
            orgGets += 1;
            return orgBody('admin');
          },
        },
      ),
    );
    renderPage();

    // The page's own role query is the only one: the header and the host
    // actions take the role from it rather than asking again.
    await screen.findByRole('button', { name: 'Run command' });
    await screen.findByRole('heading', { level: 1, name: 'web-04.fra' });
    await waitFor(() => expect(screen.getByTestId('host-facts')).toHaveTextContent('fra-1'));
    expect(orgGets).toBe(1);
  });

  it('alerts tab: open alerts with snooze for an operator, the history link, and who is notified folded away', async () => {
    const user = userEvent.setup();
    const alerts = { 'GET /api/v1/orgs/acme/alerts': { alerts: [alertBody('a1', 'warn')], next_cursor: '' } };
    let routesAsked = 0;
    mockApi(
      monitoredRoutes(
        {
          ...alerts,
          'GET /api/v1/orgs/acme/hosts/h1/alert-routes': () => {
            routesAsked += 1;
            return { routes: [] };
          },
        },
        'operator',
      ),
    );
    const { unmount } = renderTab('alerts');
    await screen.findByTestId('alert-open');
    expect(await panel().findByRole('button', { name: /^snooze$/i })).toBeInTheDocument();
    expect(panel().getByRole('button', { name: /^acknowledge$/i })).toBeInTheDocument();
    expect(panel().getByRole('link', { name: /Full history/ })).toHaveAttribute('href', '/o/acme/alerts?host=h1');

    const who = panel().getByRole('button', { name: 'Who is notified?' });
    expect(who).toHaveAttribute('aria-expanded', 'false');
    expect(panel().queryByRole('heading', { name: 'Alert routes' })).not.toBeInTheDocument();
    expect(routesAsked).toBe(0);
    await user.click(who);
    expect(who).toHaveAttribute('aria-expanded', 'true');
    expect(await panel().findByRole('heading', { name: 'Alert routes' })).toBeInTheDocument();
    unmount();

    mockApi(monitoredRoutes(alerts, 'viewer'));
    renderTab('alerts');
    await screen.findByTestId('alert-open');
    expect(screen.queryByRole('button', { name: /^snooze$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^acknowledge$/i })).not.toBeInTheDocument();
  });

  it('no clipped cards', async () => {
    mockApi(monitoredRoutes());
    const { container } = renderPage();
    await screen.findByRole('tablist');
    await screen.findByText('23 %');

    // A card that hides its overflow at a fixed height cuts its content off;
    // every card on this page grows with what it holds instead.
    expect(clippedElements(container)).toEqual([]);
  });

  it('monitored off: the hint stands in on Overview, Checks and Alerts, and nothing is measured', async () => {
    const user = userEvent.setup();
    const asked: string[] = [];
    mockApi(
      hostRoutes(
        {},
        {
          // What would apply if it were on: none of it may show.
          'GET /api/v1/orgs/acme/hosts/h1/checks': (req: MockRequest) => {
            asked.push(req.path);
            return { ...MONITORED_CHECKS, monitored: false };
          },
        },
      ),
    );
    renderPage();

    await screen.findByRole('tabpanel');
    expect(await panel().findByText('Monitoring is off')).toBeInTheDocument();
    expect(panel().getByText('No checks are measured and no alerts raised.')).toBeInTheDocument();
    // Runs and the connection keep their place on Overview.
    expect(panel().getByRole('region', { name: 'Runs' })).toBeInTheDocument();
    expect(panel().getByRole('region', { name: 'Connection' })).toBeInTheDocument();
    expect(panel().queryByRole('region', { name: 'State' })).not.toBeInTheDocument();
    expect(screen.queryByText(/still open/)).not.toBeInTheDocument();

    await user.click(tab('Checks'));
    expect(panel().getByText('Monitoring is off')).toBeInTheDocument();
    expect(panel().queryByRole('link', { name: 'Manage checks' })).not.toBeInTheDocument();
    await user.click(tab('Alerts'));
    expect(panel().getByText('Monitoring is off')).toBeInTheDocument();
    // The tabs stay, without counts.
    expect(tabItems().every(([, count]) => count === '')).toBe(true);
    expect(screen.queryByText(/cpu/i)).not.toBeInTheDocument();
    expect(asked).toEqual([]);
  });

  it('monitored off: Enable switches monitoring on', async () => {
    const user = userEvent.setup();
    let patched: unknown;
    mockApi(
      hostRoutes(
        {},
        {
          'PATCH /api/v1/orgs/acme/hosts/h1/capabilities': (req: MockRequest) => {
            patched = req.body;
            return { host: { ...HOST, monitored: true } };
          },
        },
      ),
    );
    renderPage();

    const hint = (await screen.findByText('Monitoring is off')).parentElement as HTMLElement;
    await user.click(within(hint).getByRole('button', { name: 'Enable' }));
    await waitFor(() => expect(patched).toEqual({ monitored: true }));
  });

  it('monitored off with 2 open alerts shows the link', async () => {
    mockApi(
      hostRoutes({}, { 'GET /api/v1/orgs/acme/alerts': { alerts: [alertBody('a1', 'warn'), alertBody('a2', 'crit')], next_cursor: '' } }),
    );
    renderPage();

    const link = await screen.findByRole('link', { name: '2 alerts are still open from before' });
    expect(link).toHaveAttribute('href', '/o/acme/alerts?host=h1');
    expect(screen.getByText('Monitoring off')).toBeInTheDocument();
  });

  it('managed off keeps the agent on Connection and puts the hint in Connection and Inventory', async () => {
    const user = userEvent.setup();
    let patched: unknown;
    mockApi(
      hostRoutes(
        {},
        {
          'PATCH /api/v1/orgs/acme/hosts/h1/capabilities': (req: MockRequest) => {
            patched = req.body;
            return { host: { ...HOST, managed: true } };
          },
        },
      ),
    );
    renderTab('connection');

    await screen.findByRole('tabpanel');
    expect(await panel().findByRole('heading', { name: 'Agent' })).toBeInTheDocument();
    expect(panel().getByText('Management is off')).toBeInTheDocument();
    expect(panel().queryByText('SSH access')).not.toBeInTheDocument();
    expect(panel().queryByRole('button', { name: 'Run connection test' })).not.toBeInTheDocument();

    await user.click(tab('Inventory & facts'));
    expect(panel().getByText('Management is off')).toBeInTheDocument();
    expect(panel().queryByRole('button', { name: 'Enable' })).not.toBeInTheDocument();

    await user.click(tab('Connection'));
    await user.click(panel().getByRole('button', { name: 'Enable' }));
    await waitFor(() => expect(patched).toEqual({ managed: true }));
  });

  it('renders the facts the host reported on Inventory & facts', async () => {
    mockApi(hostRoutes({ host: { facts: { os: 'linux', cpu_count: 4 } } }));
    renderTab('inventory');

    const facts = within(await screen.findByRole('region', { name: 'Facts' }));
    expect(facts.getByText('cpu_count')).toBeInTheDocument();
    expect(facts.getByText('4')).toBeInTheDocument();
  });

  it('Edit in the menu opens the dialog prefilled', async () => {
    const user = userEvent.setup();
    mockApi(hostRoutes());
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'More actions for web-04.fra' }));
    await user.click(await screen.findByRole('menuitem', { name: 'Edit…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Edit host web-04.fra' });
    expect(await within(dialog).findByLabelText('Name')).toHaveValue('web-04.fra');
    expect(within(dialog).getByLabelText('Address')).toHaveValue('203.0.113.4');
  });

  it('SSH access in the menu opens the SSH access dialog over the open tab', async () => {
    const user = userEvent.setup();
    mockApi(hostRoutes({ host: { managed: true } }));
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'More actions for web-04.fra' }));
    await user.click(await screen.findByRole('menuitem', { name: 'SSH access…' }));
    const dialog = within(await screen.findByRole('dialog', { name: 'SSH access · web-04.fra' }));
    expect(await dialog.findByLabelText('Address')).toHaveValue('203.0.113.4');
    expect(selectedTab()).toBe('Overview');
  });

  it('connection: the SSH summary with its route, and Edit opens the dialog', async () => {
    const user = userEvent.setup();
    mockApi(
      hostRoutes(
        { host: { managed: true } },
        {
          'GET /api/v1/orgs/acme/hosts/h1/ssh': { ssh: { host_id: 'h1', username: 'ops', port: 2222, credential_id: 'c1', bastion_mode: 'inherit', pin_state: 'full' } },
          'GET /api/v1/orgs/acme/credentials': {
            credentials: [{ id: 'c1', name: 'deploy-key', kind: 'ssh_key', username: 'ansible', expired: false, expires_soon: false, used_by: { hosts: 1, bastions: 0 }, created_at: '', updated_at: '' }],
          },
          'GET /api/v1/orgs/acme/bastions': { bastions: [{ id: 'b1', name: 'edge', address: 'edge.example', port: 22, username: 'ops' }] },
          'GET /api/v1/orgs/acme/hosts/h1/inventory/preview': { vars: {}, connection: { host_id: 'h1', bastion_source: 'zone', bastions: ['b1'] }, warnings: [] },
        },
      ),
    );
    renderTab('connection');
    await screen.findByRole('tabpanel');

    const card = within((await panel().findByRole('heading', { level: 3, name: 'SSH access' })).closest('div.p-4') as HTMLElement);
    expect(await card.findByText('deploy-key')).toBeInTheDocument();
    expect(card.getByText('203.0.113.4')).toBeInTheDocument();
    expect(card.getByText('2222')).toBeInTheDocument();
    expect(card.getByText('ops')).toBeInTheDocument();
    expect(card.getByText('pin complete')).toBeInTheDocument();
    expect(await card.findByLabelText('Effective route')).toHaveTextContent('worker (fra-1)→edge→ops@203.0.113.4:2222zone default');
    // The test, the agent and the pointer to the key snippets follow.
    expect(panel().getByRole('heading', { level: 3, name: 'Connection test' })).toBeInTheDocument();
    expect(panel().getByRole('heading', { level: 3, name: 'Agent' })).toBeInTheDocument();
    expect(panel().getByRole('heading', { level: 3, name: 'Key snippets' })).toBeInTheDocument();

    await user.click(card.getByRole('button', { name: 'Edit…' }));
    expect(await screen.findByRole('dialog', { name: 'SSH access · web-04.fra' })).toBeInTheDocument();
  });

  it('connection: the key snippets card opens the dialog with the snippets unfolded', async () => {
    const user = userEvent.setup();
    mockApi(hostRoutes({ host: { managed: true } }));
    renderTab('connection');
    await screen.findByRole('tabpanel');

    await user.click(await panel().findByRole('button', { name: 'Show key snippets…' }));
    const dialog = within(await screen.findByRole('dialog', { name: 'SSH access · web-04.fra' }));
    expect((await dialog.findByText('Install this key on the host')).closest('details')).toHaveAttribute('open');
  });

  it('connection: a viewer reads the SSH summary without Edit or the snippets card', async () => {
    mockApi(hostRoutes({ host: { managed: true }, role: 'viewer' }));
    renderTab('connection');
    await screen.findByRole('tabpanel');

    expect(await panel().findByRole('heading', { level: 3, name: 'SSH access' })).toBeInTheDocument();
    expect(await panel().findByText('203.0.113.4')).toBeInTheDocument();
    expect(panel().queryByRole('button', { name: 'Edit…' })).not.toBeInTheDocument();
    expect(panel().queryByRole('button', { name: 'Show key snippets…' })).not.toBeInTheDocument();
    expect(panel().queryByRole('button', { name: 'Run connection test' })).not.toBeInTheDocument();
  });

  it('deletes the host from the menu after confirmation and returns to the list', async () => {
    const user = userEvent.setup();
    mockApi(hostRoutes({}, { 'DELETE /api/v1/orgs/acme/hosts/h1': {} }));
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'More actions for web-04.fra' }));
    await user.click(await screen.findByRole('menuitem', { name: 'Delete…' }));
    await acceptConfirm(user);

    expect(await screen.findByText('HOSTS PAGE')).toBeInTheDocument();
  });

  it('surfaces a toast instead of an unhandled rejection when delete fails', async () => {
    const user = userEvent.setup();
    mockApi(
      hostRoutes(
        {},
        { 'DELETE /api/v1/orgs/acme/hosts/h1': () => new Response(JSON.stringify({ detail: 'host still managed' }), { status: 409 }) },
      ),
    );
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'More actions for web-04.fra' }));
    await user.click(await screen.findByRole('menuitem', { name: 'Delete…' }));
    await acceptConfirm(user);

    expect(await screen.findByRole('status')).toHaveTextContent('host still managed');
    expect(screen.queryByText('HOSTS PAGE')).not.toBeInTheDocument();
  });

  it('mounts the checks for a viewer who may read them', async () => {
    mockApi(
      hostRoutes(
        { role: 'viewer', host: { monitored: true } },
        {
          'GET /api/v1/orgs/acme/hosts/h1/checks': {
            host_id: 'h1',
            monitored: true,
            checks: [check('cpu', 'cpu', [reading('usage_percent', 7.5)])],
          },
        },
      ),
    );
    renderTab('checks');

    // The checks tab is wired to the host's own id and to the caller's
    // checks.read — dropping either leaves the page with no monitoring at
    // all, which is what this pins.
    const checks = within(await screen.findByRole('region', { name: 'Checks' }));
    expect(await checks.findByRole('heading', { name: 'cpu' })).toBeInTheDocument();
    // Collapsed: the row's key value, then the readings once it opens.
    expect(checks.getByText('usage_percent 7.5')).toBeInTheDocument();
    expect(checks.queryByRole('link', { name: 'Manage checks' })).not.toBeInTheDocument();
  });

  it('never claims a member is not one while the role is still in flight', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    // The host is already cached — an operator who came from the list, or
    // a second visit — so the only answer still outstanding on the first
    // paint is the org's, and the role with it. That is the state the page
    // used to render fully in, telling an admin that "monitoring is
    // visible to members of this organization" because may(undefined, …)
    // is false.
    qc.setQueryData(['host', 'acme', 'h1'], { host: { ...HOST, monitored: true } });
    const fetch = mockApi(hostRoutes({ host: { monitored: true } }));

    // The org answer is held back until this test releases it; every
    // other route answers normally, so the page has everything it needs
    // EXCEPT the role.
    const base = fetch.getMockImplementation()!;
    let release!: () => void;
    const held = new Promise<void>((resolve) => {
      release = resolve;
    });
    fetch.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : new Request(input, init);
      if (new URL(request.url, 'http://localhost').pathname === '/api/v1/orgs/acme') {
        await held;
        return new Response(JSON.stringify(orgBody()), { status: 200, headers: { 'Content-Type': 'application/json' } });
      }
      return base(input, init);
    });

    renderPage(qc, '/o/acme/hosts/h1?tab=checks');

    // The whole page waits for the role rather than deciding without one:
    // exactly one "Loading…" (the page's), no host header rendered from
    // the cached host, and above all no verdict about membership.
    expect(await screen.findByText('Loading…')).toBeInTheDocument();
    expect(screen.queryByText('web-04.fra')).not.toBeInTheDocument();
    expect(screen.queryByText(/Monitoring is visible to members/)).not.toBeInTheDocument();

    // And once the role lands the tab is rendered for real.
    release();
    expect(await screen.findByRole('region', { name: 'Checks' })).toBeInTheDocument();
    expect(screen.queryByText(/Monitoring is visible to members/)).not.toBeInTheDocument();
  });

  it('reports a failed org read instead of rendering the page without a role', async () => {
    mockApi(
      hostRoutes({}, { 'GET /api/v1/orgs/acme': () => new Response(JSON.stringify({ detail: 'org store unavailable' }), { status: 500 }) }),
    );
    renderPage();

    // A 500 on the org read has no role in it, so there is nothing to
    // derive a permission from — and no retry, no loading state and no
    // error was the one outcome that must not survive it.
    expect(await screen.findByText('Something went wrong')).toBeInTheDocument();
    expect(screen.queryByText(/Monitoring is visible to members/)).not.toBeInTheDocument();
  });

  it('shows the not-found page for a 404 instead of hanging on "Loading…" (Important 7)', async () => {
    mockApi(
      hostRoutes({}, { 'GET /api/v1/orgs/acme/hosts/h1': () => new Response(JSON.stringify({ detail: 'not found' }), { status: 404 }) }),
    );
    renderPage();

    expect(await screen.findByText('Not found')).toBeInTheDocument();
  });

  // Backlog 3.57: a client whose agent is offline is frozen by the manager.
  // The page says it is off and when its agent was last seen, keeps its last
  // state readable but muted, and the checks tab does not call every
  // verdict stale or offer to forget items that are only quiet because the
  // client is off.
  describe('client hosts', () => {
    const threeHoursAgo = () => new Date(Date.now() - 3 * 3600_000).toISOString();
    const FROZEN_CHECKS = {
      host_id: 'h1',
      monitored: true,
      checks: [
        check('cpu', 'cpu', [reading('cpu.used_pct', 97, { state: 'crit', stale: true, state_stale: true })]),
        check('disk', 'disk', [reading('used_percent', 40, { item: '/data', stale: true, state_stale: true })]),
      ],
    };
    function routes(kind: 'server' | 'client', status: 'online' | 'offline') {
      return hostRoutes(
        { host: { monitored: true, kind } },
        {
          'GET /api/v1/orgs/acme/hosts/h1/checks': FROZEN_CHECKS,
          'GET /api/v1/orgs/acme/hosts/h1/agent': agentBody(status, status === 'online' ? new Date().toISOString() : threeHoursAgo()),
          'GET /api/v1/orgs/acme/alerts': { alerts: [alertBody('a1', 'warn'), alertBody('a2', 'warn')], next_cursor: '' },
          'GET /api/v1/orgs/acme/hosts/h1/metrics': { points: [] },
        },
      );
    }

    it('says an offline client is off, when it was last seen, and its last state muted', async () => {
      mockApi(routes('client', 'offline'));
      renderPage();

      expect(await screen.findByText('Off')).toBeInTheDocument();
      expect(screen.getByText('last seen 3 h ago')).toBeInTheDocument();
      expect(await screen.findByText('last state: 2 warnings')).toBeInTheDocument();
      expect(screen.queryByText('2 warnings')).not.toBeInTheDocument();
      expect(screen.getByText(/This client is off/)).toBeInTheDocument();
      await waitFor(() => expect(document.querySelector('[data-tile]')).not.toBeNull());
      for (const tile of document.querySelectorAll('[data-tile]')) expect(tile).toHaveAttribute('data-tone', 'unknown');
    });

    it("does not call a frozen client's verdicts stale and offers no Forget", async () => {
      mockApi(routes('client', 'offline'));
      renderTab('checks');

      expect(await screen.findByText(/Not judged while the client is off/)).toBeInTheDocument();
      expect(screen.queryByText('stale verdict')).not.toBeInTheDocument();
      expect(screen.queryByRole('button', { name: /Forget/ })).not.toBeInTheDocument();
    });

    it('tags an online client and judges it like a server', async () => {
      mockApi(routes('client', 'online'));
      renderTab('checks');

      expect(await screen.findByText('Client')).toBeInTheDocument();
      expect(screen.queryByText('Off')).not.toBeInTheDocument();
      expect(await screen.findByText('2 warnings')).toBeInTheDocument();
      expect((await screen.findAllByText('stale verdict')).length).toBeGreaterThan(0);
      expect(screen.queryByText(/Not judged while the client is off/)).not.toBeInTheDocument();
    });

    it('leaves a server with an offline agent as it was', async () => {
      mockApi(routes('server', 'offline'));
      renderTab('checks');

      expect(await screen.findByText('2 warnings')).toBeInTheDocument();
      expect(screen.queryByText('Off')).not.toBeInTheDocument();
      expect(screen.queryByText('Client')).not.toBeInTheDocument();
      await expandChecks();
      expect((await screen.findAllByText('stale verdict')).length).toBeGreaterThan(0);
      expect(screen.getByRole('button', { name: 'Forget disk /data' })).toBeInTheDocument();
    });
  });
});
