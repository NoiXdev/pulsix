import { describe, expect, it } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider, useSearch } from '@tanstack/react-router';
import { mockApi, type MockRequest } from '../../../test/mockApi.ts';
import { ToastProvider } from '../../../ui/toast.tsx';
import { HostAlertsCard, HostAlertsHistoryLink } from '../HostAlertsCard.tsx';

const alert = (over: Record<string, unknown> = {}) => ({
  id: 'a1',
  host_id: 'h1',
  host_name: 'web-1',
  scope: 'tuple',
  check_key: 'disk',
  item: '/var',
  metric: 'use_pct',
  opened_at: '2026-10-01T00:00:00Z',
  severity: 'warn',
  peak_severity: 'crit',
  snooze_until_change: false,
  was_flapping: false,
  muted: false,
  ...over,
});

/** A stand-in for the real AlertsPage that reads back whatever `?host=`
 * the link actually sent — this is what lets the test below tell "the
 * link carries the host" apart from "the link merely points at the right
 * path", which a bare href-string assertion could not (fix round 2's own
 * review finding). */
function AlertsPageStub() {
  const search = useSearch({ strict: false }) as { host?: string };
  return <div>ALERTS PAGE host={search.host ?? '(none)'}</div>;
}

/** HostAlertsHistoryLink links to the org-wide Alerts page (`<Link to="/o/$org/alerts">`),
 * which needs a real router context to resolve — this builds the same
 * minimal host-route-plus-destination-route tree HostChecksCard.test.tsx
 * already uses for its own "Manage checks" link. `alertsRoute` carries the
 * same `validateSearch` the real `router.tsx` declares, so a test clicking
 * through exercises the same search-parsing the app does. */
function renderCard(canRead = true, canWrite = false) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const alertsRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/alerts',
    validateSearch: (search: Record<string, unknown>) => ({
      host: typeof search.host === 'string' ? search.host : undefined,
    }),
    component: AlertsPageStub,
  });
  const hostRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/hosts/$id',
    // The link sits beside the host page's Alerts section title, not in
    // the card; rendered together here as the page renders them.
    component: () => (
      <>
        <HostAlertsHistoryLink org="acme" hostId="h1" canRead={canRead} />
        <HostAlertsCard org="acme" hostId="h1" canRead={canRead} canWrite={canWrite} />
      </>
    ),
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([alertsRoute, hostRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/hosts/h1'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('HostAlertsCard', () => {
  it('reads the list endpoint filtered by host and to open alerts only, not a host-specific route', async () => {
    let askedSearch: Record<string, string> | undefined;
    mockApi({
      'GET /api/v1/orgs/acme/alerts': (req: MockRequest) => {
        askedSearch = Object.fromEntries(req.search.entries());
        return { alerts: [alert()] };
      },
    });
    renderCard();

    await screen.findByTestId('alert-open');
    // The request that actually landed carries `host=h1` as a query
    // parameter on the shared list route — a component that instead (or
    // additionally) called a made-up host-specific endpoint would either
    // 404 against this mock (there is no route registered for it) or
    // leave `askedSearch` unset for this one, so this assertion is the one
    // that would fail if a seventh route were invented. `state=open` is
    // pinned the same way: a card that dropped this filter would still
    // pass every other assertion in this file.
    expect(askedSearch?.host).toBe('h1');
    expect(askedSearch?.state).toBe('open');
  });

  it('renders an explicit empty state when the host has no alerts at all (it is healthy)', async () => {
    mockApi({ 'GET /api/v1/orgs/acme/alerts': { alerts: [] } });
    renderCard();

    expect(await screen.findByText(/healthy/i)).toBeInTheDocument();
    expect(screen.queryByTestId('alert-open')).not.toBeInTheDocument();
  });

  it('renders the healthy empty state for a host whose alerts are all closed, not just a host with none at all', async () => {
    // The case a "no alerts at all" fixture cannot catch: a host that HAD
    // an alert, now closed, must still read as healthy once the card asks
    // for state=open. This mock plays the server's own part in that —
    // returning the closed alert only if asked for anything other than
    // open — so a regression that dropped the state=open filter (or asked
    // for "closed") would see that alert come back and fail the assertion
    // below, instead of silently passing the way a no-alerts-at-all
    // fixture would.
    mockApi({
      'GET /api/v1/orgs/acme/alerts': (req: MockRequest) => {
        if (req.search.get('state') === 'open') return { alerts: [] };
        return { alerts: [alert({ closed_at: '2026-10-02T00:00:00Z', closed_reason: 'resolved' })] };
      },
    });
    renderCard();

    expect(await screen.findByText(/healthy/i)).toBeInTheDocument();
    expect(screen.queryByTestId('alert-open')).not.toBeInTheDocument();
  });

  it('renders the three facts for each of this host\'s open alerts: open-ness, peak severity, tracked severity', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/alerts': {
        alerts: [alert({ severity: 'warn', peak_severity: 'crit', closed_at: undefined })],
      },
    });
    renderCard();

    await screen.findByTestId('alert-open');
    expect(screen.getByTestId('alert-open')).toHaveTextContent(/open/i);
    expect(screen.getByTestId('alert-peak')).toHaveTextContent(/crit/i);
    expect(screen.getByTestId('alert-current')).toHaveTextContent(/warn/i);
  });

  it('links to the org-wide Alerts page pre-scoped to this host, not the unscoped full list', async () => {
    // The bug this test is written against: a link to /o/$org/alerts with
    // no search at all. That would still satisfy a bare `toHaveAttribute
    // ('href', '/o/acme/alerts')` assertion (fix round 2 shipped exactly
    // that, and its own test could not catch it) while actually landing
    // the operator on the unscoped list with no way back to this host's
    // history — so this asserts the href carries `?host=h1` AND that a
    // real navigation through the link lands on a page that can read it
    // back out via useSearch, not only that the string happens to match.
    const user = userEvent.setup();
    mockApi({ 'GET /api/v1/orgs/acme/alerts': { alerts: [] } });
    renderCard();

    await screen.findByText(/healthy/i);
    const link = screen.getByRole('link', { name: /full history, including closed alerts/i });
    expect(link).toHaveAttribute('href', '/o/acme/alerts?host=h1');

    await user.click(link);
    expect(await screen.findByText('ALERTS PAGE host=h1')).toBeInTheDocument();
  });

  it('asks for nothing without checks.read', async () => {
    let asked = false;
    mockApi({
      'GET /api/v1/orgs/acme/alerts': () => {
        asked = true;
        return { alerts: [] };
      },
    });
    renderCard(false);

    expect(await screen.findByText(/visible to members/i)).toBeInTheDocument();
    expect(asked).toBe(false);
    expect(screen.queryByRole('link', { name: /full history/i })).not.toBeInTheDocument();
  });

  it('shows a read error rather than hanging on "Loading…" forever', async () => {
    mockApi({ 'GET /api/v1/orgs/acme/alerts': () => new Response('{"detail":"boom"}', { status: 500 }) });
    renderCard();

    expect(await screen.findByRole('alert')).toHaveTextContent(/boom/i);
  });

  it('fetches one alert\'s timeline from the detail endpoint only once its disclosure is opened, not from a second list call', async () => {
    const user = userEvent.setup();
    let detailRequested = false;
    mockApi({
      'GET /api/v1/orgs/acme/alerts': { alerts: [alert()] },
      'GET /api/v1/orgs/acme/alerts/a1': () => {
        detailRequested = true;
        return {
          alert: {
            ...alert(),
            timeline: [{ at: '2026-10-01T00:00:00Z', kind: 'event', event: { id: 'e1', at: '2026-10-01T00:00:00Z', kind: 'opened' } }],
          },
        };
      },
    });
    renderCard();

    await screen.findByTestId('alert-open');
    expect(detailRequested).toBe(false);

    await user.click(screen.getByRole('button', { name: /show timeline/i }));
    expect(await screen.findByText(/alert opened/i)).toBeInTheDocument();
    expect(detailRequested).toBe(true);
  });

  it('renders a host-scope alert\'s timeline (events only, no state history) without an error', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme/alerts': { alerts: [alert({ scope: 'host', check_key: undefined, item: undefined, metric: undefined })] },
      'GET /api/v1/orgs/acme/alerts/a1': {
        alert: {
          ...alert({ scope: 'host', check_key: undefined, item: undefined, metric: undefined }),
          timeline: [{ at: '2026-10-01T00:00:00Z', kind: 'event', event: { id: 'e1', at: '2026-10-01T00:00:00Z', kind: 'opened' } }],
        },
      },
    });
    renderCard();

    await screen.findByTestId('alert-open');
    await user.click(screen.getByRole('button', { name: /show timeline/i }));

    expect(await screen.findByText(/alert opened/i)).toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('snoozes one of the host\'s open alerts from the host page (spec 1.2: open alerts with snooze)', async () => {
    const user = userEvent.setup();
    let sentBody: unknown;
    mockApi({
      'GET /api/v1/orgs/acme/alerts': { alerts: [alert()] },
      'POST /api/v1/orgs/acme/alerts/a1/snooze': (req: MockRequest) => {
        sentBody = req.body;
        return {};
      },
    });
    renderCard(true, true);

    await user.click(await screen.findByRole('button', { name: /^snooze$/i }));
    await user.click(await screen.findByRole('switch', { name: /severity next changes/i }));
    const save = screen.getAllByRole('button', { name: /^snooze$/i }).find((b) => b.getAttribute('type') === 'submit')!;
    await user.click(save);

    await waitFor(() => expect(sentBody).toEqual({ until_change: true }));
  });

  it('acknowledges one of the host\'s open alerts from the host page', async () => {
    const user = userEvent.setup();
    let acked = false;
    mockApi({
      'GET /api/v1/orgs/acme/alerts': { alerts: [alert()] },
      'POST /api/v1/orgs/acme/alerts/a1/acknowledge': () => {
        acked = true;
        return {};
      },
    });
    renderCard(true, true);

    await user.click(await screen.findByRole('button', { name: /^acknowledge$/i }));
    const save = (await screen.findAllByRole('button', { name: /^acknowledge$/i })).find((b) => b.getAttribute('type') === 'submit')!;
    await user.click(save);

    await waitFor(() => expect(acked).toBe(true));
  });

  it('lifts a snooze from the host page', async () => {
    const user = userEvent.setup();
    let unsnoozed = false;
    mockApi({
      'GET /api/v1/orgs/acme/alerts': { alerts: [alert({ snooze_until_change: true, muted: true, mute_reason: 'snooze' })] },
      'DELETE /api/v1/orgs/acme/alerts/a1/snooze': () => {
        unsnoozed = true;
        return {};
      },
    });
    renderCard(true, true);

    await user.click(await screen.findByRole('button', { name: /unsnooze/i }));

    await waitFor(() => expect(unsnoozed).toBe(true));
  });

  it('offers a viewer (no checks.write) no alert actions on the host page', async () => {
    mockApi({ 'GET /api/v1/orgs/acme/alerts': { alerts: [alert()] } });
    renderCard(true, false);

    await screen.findByTestId('alert-open');
    expect(screen.queryByRole('button', { name: /^acknowledge$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /snooze/i })).not.toBeInTheDocument();
  });
});
