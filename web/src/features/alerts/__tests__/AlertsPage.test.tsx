import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../../test/mockApi.ts';
import { pickDateTime } from '../../../test/pickDateTime.ts';
import { pickOption } from '../../../test/pickOption.ts';
import { AlertsPage } from '../AlertsPage.tsx';

// Only Date is faked: the calendar opens on the current month.
afterEach(() => vi.useRealTimers());
const pinClock = () => {
  vi.useFakeTimers({ toFake: ['Date'] });
  vi.setSystemTime(new Date(2026, 9, 20, 12, 0));
};

const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });

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
  peak_severity: 'warn',
  snooze_until_change: false,
  was_flapping: false,
  muted: false,
  ...over,
});

function renderPage(initialPath = '/o/acme/alerts') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  // Mirrors router.tsx's own alertsRoute exactly (fix round 2):
  // HostAlertsCard's link depends on this page actually reading `?host=`
  // back out, not just on the link carrying it.
  const alertsRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/alerts',
    validateSearch: (search: Record<string, unknown>) => ({
      host: typeof search.host === 'string' ? search.host : undefined,
    }),
    component: AlertsPage,
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([alertsRoute])]),
    history: createMemoryHistory({ initialEntries: [initialPath] }),
  });
  const view = render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
  // Returned alongside the render result (every existing call site ignores
  // this and keeps using the global `screen`, same as before) so a test
  // can read the actual browser-visible location back
  // (`router.state.location.search`) or unmount this instance to simulate
  // a reload at wherever navigation actually left the URL (fix round 3).
  return { ...view, router };
}

describe('AlertsPage', () => {
  it('lists alerts in the order the API returns them', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/alerts': {
        alerts: [
          alert({ id: 'a-crit', host_id: 'h-crit', host_name: 'db-1', check_key: 'cpu', item: '', metric: 'load', severity: 'crit', peak_severity: 'crit' }),
          alert({ id: 'a-warn', host_id: 'h-warn', host_name: 'web-2', check_key: 'disk', item: '/var', metric: 'use_pct' }),
        ],
      },
    });
    renderPage();

    await screen.findByText('cpu · load');
    expect(screen.getByText('disk · /var · use_pct')).toBeInTheDocument();
    // Each alert's own host name is on screen, and attached to its own
    // row, not the other one's — the same wrong-host regression the
    // backend's own join test guards against (a row that showed every
    // alert under one constant or swapped host name would still pass a
    // check that merely looked for "db-1" and "web-2" somewhere on the
    // page, so this pins each name to its own row's host cell instead).
    const hostCells = screen.getAllByTestId('alert-host');
    expect(hostCells[0]).toHaveTextContent('db-1');
    expect(hostCells[1]).toHaveTextContent('web-2');
    // Each row's own three facts are on screen, not merged into one cell.
    expect(screen.getAllByTestId('alert-open')).toHaveLength(2);
    expect(screen.getAllByTestId('alert-peak')).toHaveLength(2);
    expect(screen.getAllByTestId('alert-current')).toHaveLength(2);
  });

  it('renders an explicit empty state rather than a blank page', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/alerts': { alerts: [] },
    });
    renderPage();

    expect(await screen.findByText(/No alerts match this view/)).toBeInTheDocument();
  });

  // Backlog 4.37. The `muted` filter is applied server-side AFTER the
  // page is cut (alerts.Service.List), so a page can legitimately come
  // back empty with next_cursor still set — following the cursor yields
  // the full set. Correct, but the two pieces of copy contradicted each
  // other on screen: "nothing matched" above a "Load more" button that
  // would go on to produce matches.
  it('does not claim nothing matched while a cursor is still unfollowed', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/alerts': { alerts: [], next_cursor: 'cur-2' },
    });
    renderPage();

    expect(await screen.findByRole('button', { name: /Load more/i })).toBeInTheDocument();
    expect(screen.queryByText(/No alerts match this view/)).not.toBeInTheDocument();
  });

  it('asks for nothing without checks.read', async () => {
    let asked = false;
    mockApi({
      'GET /api/v1/orgs/acme': org('none'),
      'GET /api/v1/orgs/acme/alerts': () => {
        asked = true;
        return { alerts: [] };
      },
    });
    renderPage();

    expect(await screen.findByText(/visible to members/)).toBeInTheDocument();
    expect(asked).toBe(false);
  });

  it('offers no row actions for a viewer (no checks.write)', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('viewer'),
      'GET /api/v1/orgs/acme/alerts': { alerts: [alert()] },
    });
    renderPage();

    await screen.findByText('disk · /var · use_pct');
    // Anchored, not a bare /acknowledge/i: AlertFilters' own "Acknowledged"
    // picker now renders a toggle button labelled "Open Acknowledged"
    // (ui/Select.tsx, v0.4g) on every render regardless of role, and an
    // unanchored match against "Acknowledged" finds it too.
    expect(screen.queryByRole('button', { name: /^acknowledge$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^snooze$/i })).not.toBeInTheDocument();
  });

  it('applies the state filter to the alerts request', async () => {
    const asked: Array<Record<string, string>> = [];
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/alerts': (req: MockRequest) => {
        asked.push(Object.fromEntries(req.search.entries()));
        return { alerts: [] };
      },
    });
    renderPage();

    await screen.findByText(/No alerts match this view/);
    await pickOption(screen, 'State', { value: 'open' });

    await waitFor(() => expect(asked.at(-1)?.state).toBe('open'));
  });

  it('applies the severity, scope, acknowledged and muted filters to the alerts request', async () => {
    const asked: Array<Record<string, string>> = [];
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/alerts': (req: MockRequest) => {
        asked.push(Object.fromEntries(req.search.entries()));
        return { alerts: [] };
      },
    });
    renderPage();

    await screen.findByText(/No alerts match this view/);
    await pickOption(screen, 'Severity', { value: 'crit' });
    await waitFor(() => expect(asked.at(-1)?.severity).toBe('crit'));
    await pickOption(screen, 'Scope', { value: 'host' });
    await waitFor(() => expect(asked.at(-1)?.scope).toBe('host'));
    await pickOption(screen, 'Acknowledged', { value: 'true' });
    await waitFor(() => expect(asked.at(-1)?.acknowledged).toBe('true'));
    await pickOption(screen, 'Muted', { value: 'false' });
    await waitFor(() => expect(asked.at(-1)?.muted).toBe('false'));
  });

  it('acknowledges an alert and sends the note the operator typed', async () => {
    const user = userEvent.setup();
    let sentBody: unknown;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/alerts': { alerts: [alert()] },
      'POST /api/v1/orgs/acme/alerts/a1/acknowledge': (req: MockRequest) => {
        sentBody = req.body;
        return {};
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: /^acknowledge$/i }));
    const noteField = await screen.findByLabelText('Note');
    await user.type(noteField, 'Known issue, ticket OPS-42');

    // Two "Acknowledge" buttons exist at this point (the row's own trigger,
    // still in the DOM behind the native <dialog>, and the dialog's own
    // submit) — the dialog's is the one with type="submit"; clicking it is
    // what actually submits, and this assertion is the one that would fail
    // if the note were dropped (e.g. trimmed to empty, or never wired into
    // the mutation body at all) rather than merely checking that *a* POST
    // happened.
    const ackButtons = screen.getAllByRole('button', { name: /^acknowledge$/i });
    await user.click(ackButtons[ackButtons.length - 1]);

    await waitFor(() => expect(sentBody).toEqual({ note: 'Known issue, ticket OPS-42' }));
  });

  it('keeps Snooze disabled until either an instant or "until the state changes" is chosen', async () => {
    const user = userEvent.setup();
    let snoozeCalled = false;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/alerts': { alerts: [alert()] },
      'POST /api/v1/orgs/acme/alerts/a1/snooze': () => {
        snoozeCalled = true;
        return {};
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: /^snooze$/i }));
    await screen.findByRole('switch', { name: /severity next changes/i });
    // The dialog's own Save button is the one with type="submit" — the
    // row's own trigger (type="button") is still in the DOM behind the
    // native <dialog>, so a plain name query would match both. Neither
    // field has been touched yet, so Save must start disabled.
    const save = screen.getAllByRole('button', { name: /^snooze$/i }).find((b) => b.getAttribute('type') === 'submit')!;
    expect(save).toBeDisabled();

    await user.click(screen.getByRole('switch', { name: /severity next changes/i }));
    expect(save).not.toBeDisabled();

    // Unchecking it with no date entered returns to neither-set.
    await user.click(screen.getByRole('switch', { name: /severity next changes/i }));
    expect(save).toBeDisabled();

    expect(snoozeCalled).toBe(false);
  });

  it('snoozes an alert until the severity next changes, without ever having set a date', async () => {
    const user = userEvent.setup();
    let sentBody: unknown;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/alerts': { alerts: [alert()] },
      'POST /api/v1/orgs/acme/alerts/a1/snooze': (req: MockRequest) => {
        sentBody = req.body;
        return {};
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: /^snooze$/i }));
    await screen.findByRole('switch', { name: /severity next changes/i });
    await user.click(screen.getByRole('switch', { name: /severity next changes/i }));
    const save = screen.getAllByRole('button', { name: /^snooze$/i }).find((b) => b.getAttribute('type') === 'submit')!;
    await user.click(save);

    await waitFor(() => expect(sentBody).toEqual({ until_change: true }));
  });

  it('snoozes an alert until a chosen instant', async () => {
    pinClock();
    const user = userEvent.setup();
    let sentBody: unknown;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/alerts': { alerts: [alert()] },
      'POST /api/v1/orgs/acme/alerts/a1/snooze': (req: MockRequest) => {
        sentBody = req.body;
        return {};
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: /^snooze$/i }));
    await screen.findByLabelText('Until');
    await pickDateTime(user, screen, 'Until', '10:00');

    const save = screen.getAllByRole('button', { name: /^snooze$/i }).find((b) => b.getAttribute('type') === 'submit')!;
    expect(save).not.toBeDisabled();
    await user.click(save);

    await waitFor(() => expect(sentBody).toEqual({ until: new Date('2026-10-15T10:00').toISOString() }));
  });

  it('picking "until the state changes" after typing a date clears the date, the two fields being mutually exclusive', async () => {
    pinClock();
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/alerts': { alerts: [alert()] },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: /^snooze$/i }));
    const untilField = await screen.findByLabelText('Until');
    await pickDateTime(user, screen, 'Until', '10:00');
    expect(untilField).toHaveTextContent('2026');
    expect(screen.getByRole('textbox', { name: 'Time' })).toHaveValue('10:00');

    await user.click(screen.getByRole('switch', { name: /severity next changes/i }));
    // Checking the box clears the date the operator had just typed, and
    // disables the field — a dialog that left the old date sitting in a
    // disabled input, or worse still submitted it alongside until_change,
    // would send the server a body it 422s on.
    expect(untilField).not.toHaveTextContent('2026');
    expect(screen.getByRole('textbox', { name: 'Time' })).toHaveValue('');
    expect(untilField).toBeDisabled();
  });

  it('unacknowledges a covered alert directly, with no dialog', async () => {
    const user = userEvent.setup();
    let called = false;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/alerts': {
        alerts: [alert({ ack_at: '2026-10-01T01:00:00Z', ack_severity: 'warn', ack_by: 'u1' })],
      },
      'DELETE /api/v1/orgs/acme/alerts/a1/acknowledge': () => {
        called = true;
        return {};
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: /unacknowledge/i }));
    await waitFor(() => expect(called).toBe(true));
  });

  it('opens pre-filtered to one host when the URL carries ?host= (HostAlertsCard\'s own link)', async () => {
    // Fix round 2: this is the other half of HostAlertsCard's link — the
    // link carrying `?host=` is worthless if this page does not actually
    // read it back into its own filter and request. A version that kept
    // starting `filter` at a bare `{}` would send no `host` at all here
    // and still pass every other test in this file.
    let askedSearch: Record<string, string> | undefined;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/alerts': (req: MockRequest) => {
        askedSearch = Object.fromEntries(req.search.entries());
        return { alerts: [] };
      },
    });
    renderPage('/o/acme/alerts?host=h1');

    await screen.findByText(/No alerts match this view/);
    expect(askedSearch?.host).toBe('h1');
    // The operator must be able to SEE the list is narrowed, not just have
    // it silently be shorter than expected (fix round 2's own review
    // finding) — and must have a way back to the unscoped view.
    expect(screen.getByText(/one host's alerts only/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /show every alert/i })).toBeInTheDocument();
  });

  it('clears the host scope and re-requests every alert when "Show every alert" is clicked', async () => {
    const user = userEvent.setup();
    const asked: Array<Record<string, string>> = [];
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/alerts': (req: MockRequest) => {
        asked.push(Object.fromEntries(req.search.entries()));
        return { alerts: [] };
      },
    });
    const { router } = renderPage('/o/acme/alerts?host=h1');

    await screen.findByText(/one host's alerts only/i);
    expect(asked.at(-1)?.host).toBe('h1');

    await user.click(screen.getByRole('button', { name: /show every alert/i }));

    await waitFor(() => expect(asked.at(-1)?.host).toBe(''));
    expect(screen.queryByText(/one host's alerts only/i)).not.toBeInTheDocument();
    // Fix round 3: clearing the in-memory filter is not enough on its own
    // — a version that called only setFilter, never navigate, would still
    // pass every assertion above (the request and the notice both read
    // off `filter`, not off the URL) while leaving `router.state.location`
    // at `?host=h1`. This is the assertion that tells the two apart: the
    // browser's own address bar must agree that the host scope is gone.
    await waitFor(() => expect(router.state.location.search).toEqual({ host: undefined }));
  });

  it('does not re-impose a cleared host scope on reload, because the URL itself was cleared, not just local state', async () => {
    // The exact bug the previous test's new assertion alone cannot fully
    // dramatize: if "Show every alert" left `?host=h1` in the address bar,
    // a reload — simulated here by unmounting and mounting a fresh
    // AlertsPage at wherever the URL actually pointed afterwards — runs
    // the render-time sync that seeds `filter` from the URL from scratch,
    // silently re-applying the very scope the operator had just cleared.
    // A correct fix must leave the URL itself clear, not only this one
    // mount's in-memory state.
    const user = userEvent.setup();
    const asked: Array<Record<string, string>> = [];
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/alerts': (req: MockRequest) => {
        asked.push(Object.fromEntries(req.search.entries()));
        return { alerts: [] };
      },
    });
    const { router, unmount } = renderPage('/o/acme/alerts?host=h1');

    await screen.findByText(/one host's alerts only/i);
    await user.click(screen.getByRole('button', { name: /show every alert/i }));
    await waitFor(() => expect(asked.at(-1)?.host).toBe(''));

    const hrefAfterClear = router.history.location.href;
    unmount();
    renderPage(hrefAfterClear);

    await screen.findByText(/No alerts match this view/);
    expect(screen.queryByText(/one host's alerts only/i)).not.toBeInTheDocument();
    expect(asked.at(-1)?.host).toBe('');
  });
  it('discloses a closed alert\u2019s timeline, which is otherwise unreachable in the GUI', async () => {
    // The design doc opens with "nobody can yet say host X was critical
    // from 14:03 to 14:51 on Tuesday, Anna acknowledged it at 14:12".
    // HostAlertsCard shows open alerts only, so before this disclosure the
    // API could answer that and the GUI could not, for any alert that had
    // ended (review finding, Important). The fixture is deliberately a
    // CLOSED alert, the case the host card can never render.
    const user = userEvent.setup();
    let detailCalls = 0;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/alerts': {
        alerts: [alert({ id: 'a-closed', closed_at: '2026-10-01T14:51:00Z', closed_reason: 'resolved' })],
      },
      'GET /api/v1/orgs/acme/alerts/a-closed': () => {
        detailCalls += 1;
        return {
          alert: {
            ...alert({ id: 'a-closed', closed_at: '2026-10-01T14:51:00Z', closed_reason: 'resolved' }),
            timeline: [
              { at: '2026-10-01T14:03:00Z', kind: 'event', event: { kind: 'opened' } },
              { at: '2026-10-01T14:12:00Z', kind: 'event', event: { kind: 'acknowledged' } },
              { at: '2026-10-01T14:51:00Z', kind: 'event', event: { kind: 'closed' } },
            ],
          },
        };
      },
    });
    renderPage();

    const toggle = await screen.findByRole('button', { name: /timeline for web-1/i });
    // Nothing is fetched until the row is actually expanded — the same
    // "pay for it when it is asked for" rule HostAlertsCard holds.
    expect(detailCalls).toBe(0);

    await user.click(toggle);
    await waitFor(() => expect(detailCalls).toBe(1));
    expect(toggle).toHaveAttribute('aria-expanded', 'true');
    // "Alert opened" and "Closed — resolved …" exist only on the
    // timeline; the row itself renders neither, so these prove the panel
    // and not the row.
    await screen.findByText('Alert opened');
    expect(screen.getByText(/Closed — resolved/)).toBeInTheDocument();

    await user.click(toggle);
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
  });

  it('follows next_cursor rather than stopping at the first page', async () => {
    // The list is unbounded over a table the design gives no retention, so
    // it is paginated (review finding, Important). "Load more" must follow
    // the server's own cursor, and must disappear once the server stops
    // handing one back.
    const user = userEvent.setup();
    const cursors: Array<string | null> = [];
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/alerts': (req: MockRequest) => {
        const cursor = req.search.get('cursor');
        cursors.push(cursor);
        if (!cursor) {
          return { alerts: [alert({ id: 'a-page1', host_name: 'web-1' })], next_cursor: 'cur-2' };
        }
        return { alerts: [alert({ id: 'a-page2', host_name: 'web-9' })], next_cursor: '' };
      },
    });
    renderPage();

    await screen.findByText('web-1');
    expect(screen.queryByText('web-9')).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: /load more/i }));

    await screen.findByText('web-9');
    expect(screen.getByText('web-1')).toBeInTheDocument();
    expect(cursors).toEqual([null, 'cur-2']);
    await waitFor(() => expect(screen.queryByRole('button', { name: /load more/i })).not.toBeInTheDocument());
  });
});
