import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/react-query';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { alertBody, check, HOST, hostRoutes, orgBody, reading, renderAt } from '../../test/hostDetail.tsx';
import { clippedElements, isClipped } from '../../test/clipped.ts';
import { HostDetailPage } from './HostDetailPage.tsx';

const renderPage = (qc?: QueryClient) => renderAt(HostDetailPage, qc);

/** The section nav, as label → count ('' for an item without one). */
function navItems(): Array<[string, string]> {
  const nav = screen.getByRole('navigation');
  return within(nav)
    .getAllByRole('link')
    .map((a) => {
      const spans = a.querySelectorAll('span');
      return [spans[0]?.textContent ?? '', spans[1]?.textContent ?? ''];
    });
}

const MONITORED_CHECKS = {
  host_id: 'h1',
  monitored: true,
  checks: [check('cpu', 'cpu', [reading('cpu.used_pct', 23)]), check('memory', 'memory', [reading('mem.used_pct', 61)])],
};

describe('HostDetailPage', () => {
  it('renders sidebar facts and section nav', async () => {
    mockApi(
      hostRoutes(
        { host: { managed: true, monitored: true } },
        {
          'GET /api/v1/orgs/acme/hosts/h1/checks': MONITORED_CHECKS,
          'GET /api/v1/orgs/acme/alerts': { alerts: [alertBody('a1', 'warn'), alertBody('a2', 'warn')], next_cursor: '' },
          'GET /api/v1/orgs/acme/hosts/h1/metrics': { points: [] },
        },
      ),
    );
    renderPage();

    expect(await screen.findByRole('heading', { level: 1, name: 'web-04.fra' })).toBeInTheDocument();
    const facts = within(screen.getByTestId('host-facts'));
    expect(facts.getByText('Address')).toBeInTheDocument();
    expect(facts.getByText('203.0.113.4')).toBeInTheDocument();
    expect(await facts.findByText('fra-1')).toBeInTheDocument();
    expect(facts.getByText('debian 13')).toBeInTheDocument();
    expect(await screen.findByText('2 warnings')).toBeInTheDocument();

    await waitFor(() =>
      expect(navItems()).toEqual([
        ['State', ''],
        ['Alerts', '2'],
        ['Checks', '2'],
        ['Runs', ''],
        ['Connection & SSH', ''],
        ['Inventory', ''],
        ['Facts', ''],
      ]),
    );
    const alertsLink = within(screen.getByRole('navigation')).getByRole('link', { name: /Alerts/ });
    expect(alertsLink).toHaveAttribute('href', '#alerts');
    for (const id of ['state', 'alerts', 'checks', 'runs', 'connection', 'inventory', 'facts']) {
      expect(document.getElementById(id)).not.toBeNull();
    }
    // The section titles name the cards; the cards no longer repeat them.
    expect(screen.queryByRole('heading', { name: 'Monitoring' })).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Manage checks' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /Full history/ })).toHaveAttribute('href', '/o/acme/alerts?host=h1');
  });

  it('titles the sections at level 2 and the cards inside them at level 3', async () => {
    mockApi(
      hostRoutes(
        { host: { managed: true, monitored: true } },
        {
          'GET /api/v1/orgs/acme/hosts/h1/checks': MONITORED_CHECKS,
          'GET /api/v1/orgs/acme/hosts/h1/metrics': { points: [] },
        },
      ),
    );
    renderPage();

    const connection = within(await screen.findByRole('region', { name: 'Connection & SSH' }));
    expect(await connection.findByRole('heading', { level: 3, name: 'SSH access' })).toBeInTheDocument();
    expect(connection.getByRole('heading', { level: 3, name: 'Agent' })).toBeInTheDocument();
    expect(connection.queryByRole('heading', { level: 2, name: 'SSH access' })).not.toBeInTheDocument();
    await screen.findByText('23 %');

    const sections = ['State', 'Alerts', 'Checks', 'Runs', 'Connection & SSH', 'Inventory', 'Facts'];
    expect(screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)).toEqual(sections);
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

    // The page's own role query is the only one: the sidebar and the host
    // actions take the role from it rather than asking again.
    await screen.findByRole('button', { name: 'Run command' });
    await screen.findByRole('heading', { level: 1, name: 'web-04.fra' });
    await waitFor(() => expect(screen.getByTestId('host-facts')).toHaveTextContent('fra-1'));
    expect(orgGets).toBe(1);
  });

  it('offers snooze on the Alerts section to checks.write and nothing to a viewer', async () => {
    const routes = (role: string) =>
      hostRoutes(
        { host: { managed: true, monitored: true }, role },
        {
          'GET /api/v1/orgs/acme/hosts/h1/checks': MONITORED_CHECKS,
          'GET /api/v1/orgs/acme/alerts': { alerts: [alertBody('a1', 'warn')], next_cursor: '' },
          'GET /api/v1/orgs/acme/hosts/h1/metrics': { points: [] },
        },
      );
    mockApi(routes('operator'));
    const { unmount } = renderPage();
    const alerts = await screen.findByTestId('alert-open');
    const section = alerts.closest('section') ?? document.getElementById('alerts')!;
    expect(await within(section).findByRole('button', { name: /^snooze$/i })).toBeInTheDocument();
    expect(within(section).getByRole('button', { name: /^acknowledge$/i })).toBeInTheDocument();
    unmount();

    mockApi(routes('viewer'));
    renderPage();
    await screen.findByTestId('alert-open');
    expect(screen.queryByRole('button', { name: /^snooze$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^acknowledge$/i })).not.toBeInTheDocument();
  });

  it('no clipped cards', async () => {
    mockApi(
      hostRoutes(
        { host: { managed: true, monitored: true } },
        { 'GET /api/v1/orgs/acme/hosts/h1/checks': MONITORED_CHECKS, 'GET /api/v1/orgs/acme/hosts/h1/metrics': { points: [] } },
      ),
    );
    const { container } = renderPage();
    await screen.findByRole('navigation');
    await screen.findByText('23 %');

    // A card that hides its overflow at a fixed height cuts its content off;
    // every card on this page grows with what it holds instead.
    expect(clippedElements(container)).toEqual([]);
  });

  it.each([
    { classes: 'overflow-hidden h-40', want: true },
    { classes: 'overflow-clip max-h-[300px]', want: true },
    { classes: 'overflow-y-hidden h-64', want: true },
    { classes: 'md:overflow-y-hidden lg:max-h-96', want: true },
    { classes: 'overflow-y-auto max-h-48', want: false },
    { classes: 'overflow-hidden rounded-md', want: false },
    { classes: 'h-8 w-8', want: false },
  ])('the clipped-card guard reads "$classes" as clipped: $want', ({ classes, want }) => {
    expect(isClipped(classes.split(' '))).toBe(want);
  });

  it('monitored off hides state, alerts and checks and shows the hint', async () => {
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

    expect(await screen.findByText('Monitoring is off')).toBeInTheDocument();
    expect(screen.getByText('No checks are measured and no alerts raised.')).toBeInTheDocument();
    expect(screen.queryByText(/cpu/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/still open/)).not.toBeInTheDocument();
    await waitFor(() => expect(navItems().map(([label]) => label)).toEqual(['Runs', 'Connection & SSH', 'Inventory', 'Facts']));
    for (const id of ['state', 'alerts', 'checks']) expect(document.getElementById(id)).toBeNull();
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
    // The nav carries no counts while monitoring is off.
    expect(navItems().every(([, count]) => count === '')).toBe(true);
  });

  it('managed off keeps the agent in Connection and replaces SSH and Inventory with the hint', async () => {
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
    renderPage();

    const connection = within(await screen.findByRole('region', { name: 'Connection & SSH' }));
    expect(await connection.findByRole('heading', { name: 'Agent' })).toBeInTheDocument();
    expect(connection.getByText('Management is off')).toBeInTheDocument();
    expect(connection.queryByText('SSH access')).not.toBeInTheDocument();
    expect(connection.queryByRole('button', { name: 'Run connection test' })).not.toBeInTheDocument();

    const inventory = within(screen.getByRole('region', { name: 'Inventory' }));
    expect(inventory.getByText('Management is off')).toBeInTheDocument();
    expect(inventory.queryByRole('button', { name: 'Enable' })).not.toBeInTheDocument();

    await user.click(connection.getByRole('button', { name: 'Enable' }));
    await waitFor(() => expect(patched).toEqual({ managed: true }));
  });

  it('renders the facts the host reported', async () => {
    mockApi(hostRoutes({ host: { facts: { os: 'linux', cpu_count: 4 } } }));
    renderPage();

    const facts = within(await screen.findByRole('region', { name: 'Facts' }));
    expect(facts.getByText('cpu_count')).toBeInTheDocument();
    expect(facts.getByText('4')).toBeInTheDocument();
  });

  it('Edit opens the dialog prefilled', async () => {
    const user = userEvent.setup();
    mockApi(hostRoutes());
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'Edit' }));
    const dialog = await screen.findByRole('dialog', { name: 'Edit host web-04.fra' });
    expect(await within(dialog).findByLabelText('Name')).toHaveValue('web-04.fra');
    expect(within(dialog).getByLabelText('Address')).toHaveValue('203.0.113.4');
  });

  it('deletes the host from the menu after confirmation and returns to the list', async () => {
    const user = userEvent.setup();
    const originalConfirm = window.confirm;
    window.confirm = () => true;
    try {
      mockApi(hostRoutes({}, { 'DELETE /api/v1/orgs/acme/hosts/h1': {} }));
      renderPage();

      await user.click(await screen.findByRole('button', { name: 'More actions for web-04.fra' }));
      await user.click(await screen.findByRole('menuitem', { name: 'Delete…' }));

      expect(await screen.findByText('HOSTS PAGE')).toBeInTheDocument();
    } finally {
      window.confirm = originalConfirm;
    }
  });

  it('surfaces a toast instead of an unhandled rejection when delete fails', async () => {
    const user = userEvent.setup();
    const originalConfirm = window.confirm;
    window.confirm = () => true;
    try {
      mockApi(
        hostRoutes(
          {},
          { 'DELETE /api/v1/orgs/acme/hosts/h1': () => new Response(JSON.stringify({ detail: 'host still managed' }), { status: 409 }) },
        ),
      );
      renderPage();

      await user.click(await screen.findByRole('button', { name: 'More actions for web-04.fra' }));
      await user.click(await screen.findByRole('menuitem', { name: 'Delete…' }));

      expect(await screen.findByRole('status')).toHaveTextContent('host still managed');
      expect(screen.queryByText('HOSTS PAGE')).not.toBeInTheDocument();
    } finally {
      window.confirm = originalConfirm;
    }
  });

  it('mounts the monitoring sections for a viewer who may read checks', async () => {
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
    renderPage();

    // The checks section is wired to the host's own id and to the caller's
    // checks.read — dropping either leaves the page with no monitoring at
    // all, which is what this pins.
    const checks = within(await screen.findByRole('region', { name: 'Checks' }));
    expect(await checks.findByRole('heading', { name: 'cpu' })).toBeInTheDocument();
    expect(checks.getByText('7.5')).toBeInTheDocument();
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

    renderPage(qc);

    // The whole page waits for the role rather than deciding without one:
    // exactly one "Loading…" (the page's), no host header rendered from
    // the cached host, and above all no verdict about membership.
    expect(await screen.findByText('Loading…')).toBeInTheDocument();
    expect(screen.queryByText('web-04.fra')).not.toBeInTheDocument();
    expect(screen.queryByText(/Monitoring is visible to members/)).not.toBeInTheDocument();

    // And once the role lands the section is rendered for real.
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
});
