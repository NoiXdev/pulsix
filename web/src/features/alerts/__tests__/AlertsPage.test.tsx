import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../../test/mockApi.ts';
import { pickDateTime } from '../../../test/pickDateTime.ts';
import { pickOption } from '../../../test/pickOption.ts';
import { renderListPage } from '../../../test/renderListPage.tsx';
import type { Locale } from '../../../i18n/types.ts';
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

const PREFS = { density: 'standard', page_size: 25, lists: {} };

/** The alerts list's own routes plus the preferences every list reads. */
const base = (role = 'operator') => ({
  'GET /api/v1/auth/me/preferences': PREFS,
  'GET /api/v1/orgs/acme': org(role),
});

/** The list as the page mounts it in router.tsx's alertsRoute (fix round 2:
 * HostAlertsCard's link depends on this page reading `?host=` back out). */
function renderPage(initialPath = '/o/acme/alerts', locale?: Locale) {
  // renderListPage mounts the page itself; the unmount of the previous
  // test's tree is RTL's own cleanup.
  const router = renderListPage({
    path: '/alerts',
    component: AlertsPage,
    initial: initialPath,
    extraSearch: (search) => ({ host: typeof search.host === 'string' ? search.host : undefined }),
    locale,
  });
  // Returned so a test can read the actual browser-visible location back
  // (`router.state.location.search`) or remount at wherever navigation
  // actually left the URL (fix round 3).
  return { router };
}

/** Opens a row's "…" menu: the row is named by its host and what it is about. */
async function openMenu(user: ReturnType<typeof userEvent.setup>, name = 'Actions for web-1, disk · /var · use_pct') {
  await user.click(await screen.findByRole('button', { name }));
}

const menuItem = (name: string) => screen.findByRole('menuitem', { name });

/** n open alerts on distinct hosts, and the answer for one page of them. */
const manyAlerts = (n: number) => Array.from({ length: n }, (_, i) => alert({ id: `a${i + 1}`, host_id: `h${i + 1}`, host_name: `host-${String(i + 1).padStart(2, '0')}` }));
function pageOf(all: ReturnType<typeof manyAlerts>, req: MockRequest) {
  const page = Number(req.search.get('page') ?? '1');
  const perPage = Number(req.search.get('per_page') ?? '25');
  return { alerts: all.slice((page - 1) * perPage, page * perPage), next_cursor: '', total: all.length };
}

describe('AlertsPage', () => {
  it('lists alerts in the order the API returns them', async () => {
    mockApi({
      ...base('operator'),
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
      ...base('operator'),
      'GET /api/v1/orgs/acme/alerts': { alerts: [] },
    });
    renderPage();

    expect(await screen.findByText(/No alerts match this view/)).toBeInTheDocument();
  });

  it('asks for nothing without checks.read', async () => {
    let asked = false;
    mockApi({
      ...base('none'),
      'GET /api/v1/orgs/acme/alerts': () => {
        asked = true;
        return { alerts: [] };
      },
    });
    renderPage();

    expect(await screen.findByText(/visible to members/)).toBeInTheDocument();
    expect(asked).toBe(false);
  });

  it('offers a viewer (no alerts.handle) the timeline only', async () => {
    const user = userEvent.setup();
    mockApi({
      ...base('viewer'),
      'GET /api/v1/orgs/acme/alerts': { alerts: [alert()] },
    });
    renderPage();

    await screen.findByText('disk · /var · use_pct');
    // No inline buttons any more, and nothing to acknowledge or snooze in
    // the menu either.
    expect(screen.queryByRole('button', { name: /^acknowledge$/i })).not.toBeInTheDocument();
    await openMenu(user);
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Show timeline']);
  });

  it('offers an operator Acknowledge, Snooze and the timeline, in that order, on an open alert', async () => {
    const user = userEvent.setup();
    mockApi({ ...base('operator'), 'GET /api/v1/orgs/acme/alerts': { alerts: [alert()] } });
    renderPage();

    await screen.findByText('disk · /var · use_pct');
    // The actions are not visible buttons on the row any more.
    expect(screen.queryByRole('button', { name: /^acknowledge$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^snooze$/i })).not.toBeInTheDocument();
    await openMenu(user);
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Acknowledge', 'Snooze', 'Show timeline']);
  });

  it('offers Unsnooze instead of Snooze while a snooze mutes the alert', async () => {
    const user = userEvent.setup();
    let called = false;
    mockApi({
      ...base('operator'),
      'GET /api/v1/orgs/acme/alerts': { alerts: [alert({ muted: true, mute_reason: 'snooze', muted_until: '2026-10-08T12:00:00Z' })] },
      'DELETE /api/v1/orgs/acme/alerts/a1/snooze': () => {
        called = true;
        return {};
      },
    });
    renderPage();

    await openMenu(user);
    expect(screen.queryByRole('menuitem', { name: 'Snooze' })).not.toBeInTheDocument();
    await user.click(await menuItem('Unsnooze'));
    await waitFor(() => expect(called).toBe(true));
  });

  it('offers a closed alert no acknowledge or snooze, only the timeline', async () => {
    const user = userEvent.setup();
    mockApi({
      ...base('operator'),
      'GET /api/v1/orgs/acme/alerts': { alerts: [alert({ closed_at: '2026-10-02T00:00:00Z', closed_reason: 'resolved' })] },
    });
    renderPage();

    await openMenu(user);
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Show timeline']);
  });

  it('opens the same entries on a right-click', async () => {
    mockApi({ ...base('operator'), 'GET /api/v1/orgs/acme/alerts': { alerts: [alert()] } });
    renderPage();

    await screen.findByText('disk · /var · use_pct');
    fireEvent.contextMenu(screen.getByTestId('alert-ack'), { clientX: 200, clientY: 300 });
    const items = within(await screen.findByRole('menu')).getAllByRole('menuitem');
    expect(items.map((i) => i.textContent)).toEqual(['Acknowledge', 'Snooze', 'Show timeline']);
  });

  it('applies the state filter to the alerts request', async () => {
    const asked: Array<Record<string, string>> = [];
    mockApi({
      ...base('operator'),
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
      ...base('operator'),
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
      ...base('operator'),
      'GET /api/v1/orgs/acme/alerts': { alerts: [alert()] },
      'POST /api/v1/orgs/acme/alerts/a1/acknowledge': (req: MockRequest) => {
        sentBody = req.body;
        return {};
      },
    });
    renderPage();

    await openMenu(user);
    await user.click(await menuItem('Acknowledge'));
    const noteField = await screen.findByLabelText('Note');
    await user.type(noteField, 'Known issue, ticket OPS-42');

    // The dialog's own submit is the one with type="submit"; clicking it is
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
      ...base('operator'),
      'GET /api/v1/orgs/acme/alerts': { alerts: [alert()] },
      'POST /api/v1/orgs/acme/alerts/a1/snooze': () => {
        snoozeCalled = true;
        return {};
      },
    });
    renderPage();

    await openMenu(user);
    await user.click(await menuItem('Snooze'));
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
      ...base('operator'),
      'GET /api/v1/orgs/acme/alerts': { alerts: [alert()] },
      'POST /api/v1/orgs/acme/alerts/a1/snooze': (req: MockRequest) => {
        sentBody = req.body;
        return {};
      },
    });
    renderPage();

    await openMenu(user);
    await user.click(await menuItem('Snooze'));
    await screen.findByRole('switch', { name: /severity next changes/i });
    await user.click(screen.getByRole('switch', { name: /severity next changes/i }));
    const save = screen.getAllByRole('button', { name: /^snooze$/i }).find((b) => b.getAttribute('type') === 'submit')!;
    await user.click(save);

    await waitFor(() => expect(sentBody).toEqual({ until_change: true }));
  });

  // Final review #2: a time typed with no day is half a value, which marks
  // the field invalid — but switching to "until the severity changes"
  // disables the field, and a disabled field must not block the submit.
  it('snoozes until the severity changes after only a time was typed', async () => {
    const user = userEvent.setup();
    let sentBody: unknown;
    mockApi({
      ...base('operator'),
      'GET /api/v1/orgs/acme/alerts': { alerts: [alert()] },
      'POST /api/v1/orgs/acme/alerts/a1/snooze': (req: MockRequest) => {
        sentBody = req.body;
        return {};
      },
    });
    renderPage();

    await openMenu(user);
    await user.click(await menuItem('Snooze'));
    await user.type(await screen.findByRole('textbox', { name: 'Until Time' }), '10:00');
    await user.click(screen.getByRole('switch', { name: /severity next changes/i }));
    const save = screen.getAllByRole('button', { name: /^snooze$/i }).find((b) => b.getAttribute('type') === 'submit')!;
    expect(save).toBeEnabled();
    await user.click(save);

    await waitFor(() => expect(sentBody).toEqual({ until_change: true }));
  });

  it('snoozes an alert until a chosen instant', async () => {
    pinClock();
    const user = userEvent.setup();
    let sentBody: unknown;
    mockApi({
      ...base('operator'),
      'GET /api/v1/orgs/acme/alerts': { alerts: [alert()] },
      'POST /api/v1/orgs/acme/alerts/a1/snooze': (req: MockRequest) => {
        sentBody = req.body;
        return {};
      },
    });
    renderPage();

    await openMenu(user);
    await user.click(await menuItem('Snooze'));
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
      ...base('operator'),
      'GET /api/v1/orgs/acme/alerts': { alerts: [alert()] },
    });
    renderPage();

    await openMenu(user);
    await user.click(await menuItem('Snooze'));
    const untilField = await screen.findByLabelText('Until');
    await pickDateTime(user, screen, 'Until', '10:00');
    expect(untilField).toHaveTextContent('10/15/2026');
    expect(screen.getByRole('textbox', { name: 'Until Time' })).toHaveValue('10:00');

    await user.click(screen.getByRole('switch', { name: /severity next changes/i }));
    // Checking the box clears the date the operator had just typed, and
    // disables the field — a dialog that left the old date sitting in a
    // disabled input, or worse still submitted it alongside until_change,
    // would send the server a body it 422s on.
    expect(untilField).not.toHaveTextContent('10/15/2026');
    expect(screen.getByRole('textbox', { name: 'Until Time' })).toHaveValue('');
    expect(untilField).toBeDisabled();
  });

  it('unacknowledges a covered alert directly, with no dialog', async () => {
    const user = userEvent.setup();
    let called = false;
    mockApi({
      ...base('operator'),
      'GET /api/v1/orgs/acme/alerts': {
        alerts: [alert({ ack_at: '2026-10-01T01:00:00Z', ack_severity: 'warn', ack_by: 'u1' })],
      },
      'DELETE /api/v1/orgs/acme/alerts/a1/acknowledge': () => {
        called = true;
        return {};
      },
    });
    renderPage();

    await openMenu(user);
    await user.click(await menuItem('Unacknowledge'));
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
      ...base('operator'),
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
      ...base('operator'),
      'GET /api/v1/orgs/acme/alerts': (req: MockRequest) => {
        asked.push(Object.fromEntries(req.search.entries()));
        return { alerts: [] };
      },
    });
    const { router } = renderPage('/o/acme/alerts?host=h1');

    await screen.findByText(/one host's alerts only/i);
    expect(asked.at(-1)?.host).toBe('h1');

    await user.click(screen.getByRole('button', { name: /show every alert/i }));

    await waitFor(() => expect(asked.at(-1)?.host).toBeUndefined());
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
      ...base('operator'),
      'GET /api/v1/orgs/acme/alerts': (req: MockRequest) => {
        asked.push(Object.fromEntries(req.search.entries()));
        return { alerts: [] };
      },
    });
    const { router } = renderPage('/o/acme/alerts?host=h1');

    await screen.findByText(/one host's alerts only/i);
    await user.click(screen.getByRole('button', { name: /show every alert/i }));
    await waitFor(() => expect(asked.at(-1)?.host).toBeUndefined());

    const hrefAfterClear = router.history.location.href;
    cleanup();
    renderPage(hrefAfterClear);

    await screen.findByText(/No alerts match this view/);
    expect(screen.queryByText(/one host's alerts only/i)).not.toBeInTheDocument();
    expect(asked.at(-1)?.host).toBeUndefined();
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
      ...base('operator'),
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

    await screen.findByText('disk · /var · use_pct');
    // Nothing is fetched until the timeline is actually asked for — the
    // same "pay for it when it is asked for" rule HostAlertsCard holds.
    expect(detailCalls).toBe(0);

    await openMenu(user);
    await user.click(await menuItem('Show timeline'));
    await waitFor(() => expect(detailCalls).toBe(1));
    const dialog = await screen.findByRole('dialog', { name: 'Timeline for web-1' });
    // "Alert opened" and "Closed — resolved …" exist only on the
    // timeline; the row itself renders neither, so these prove the panel
    // and not the row.
    await within(dialog).findByText('Alert opened');
    expect(within(dialog).getByText(/Closed — resolved/)).toBeInTheDocument();

    // A mouse or touch user can close it: the dialog has no other control.
    await user.click(within(dialog).getByRole('button', { name: 'Close' }));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Timeline for web-1' })).not.toBeInTheDocument());
  });

});

describe('AlertsPage paging and sorting', () => {
  let requests: URLSearchParams[] = [];
  const setup = (all: ReturnType<typeof manyAlerts>, initial = '/o/acme/alerts', locale?: Locale) => {
    requests = [];
    mockApi({
      ...base('operator'),
      'GET /api/v1/orgs/acme/alerts': (req: MockRequest) => {
        requests.push(req.search);
        return pageOf(all, req);
      },
    });
    return renderPage(initial, locale);
  };

  it('asks for page 1 at the saved size, with no sort and no cursor, and shows the footer instead of "Load more"', async () => {
    setup(manyAlerts(30));

    expect(await screen.findByText('1–25 of 30 alerts')).toBeInTheDocument();
    expect(requests).toHaveLength(1);
    expect(requests[0].get('page')).toBe('1');
    expect(requests[0].get('per_page')).toBe('25');
    expect(requests[0].get('sort')).toBeNull();
    expect(requests[0].get('order')).toBeNull();
    expect(requests[0].get('cursor')).toBeNull();
    expect(requests[0].get('limit')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument();
  });

  it('sorts by Open, Alert (host), Peak, Tracked and Acknowledged through the URL; Mute does not sort', async () => {
    const user = userEvent.setup();
    const { router } = setup(manyAlerts(30));
    await screen.findByText('1–25 of 30 alerts');
    expect(within(screen.getByRole('columnheader', { name: /^Mute/ })).queryByRole('button')).not.toBeInTheDocument();

    for (const [header, key] of [
      ['Open', 'opened'],
      ['Alert', 'host'],
      ['Peak', 'peak'],
      ['Tracked', 'tracked'],
      ['Acknowledged', 'acknowledged'],
    ]) {
      await user.click(within(screen.getByRole('columnheader', { name: new RegExp(`^${header}`) })).getByRole('button'));
      await waitFor(() => expect(requests.at(-1)?.get('sort')).toBe(key));
      expect(requests.at(-1)?.get('order')).toBe('asc');
      expect(router.state.location.search).toMatchObject({ sort: key, order: 'asc' });
    }
  });

  it('reads the sort and page from the URL, with the host scope next to them', async () => {
    setup(manyAlerts(30), '/o/acme/alerts?sort=peak&order=desc&page=2&host=h1');
    expect(await screen.findByText('26–30 of 30 alerts')).toBeInTheDocument();
    expect(requests[0].get('sort')).toBe('peak');
    expect(requests[0].get('order')).toBe('desc');
    expect(requests[0].get('page')).toBe('2');
    expect(requests[0].get('host')).toBe('h1');
  });

  it('shows the last page for a page past the end, not the empty state', async () => {
    setup(manyAlerts(30), '/o/acme/alerts?page=9');
    expect(await screen.findByText('26–30 of 30 alerts')).toBeInTheDocument();
    expect(screen.queryByText(/No alerts match this view/)).not.toBeInTheDocument();
  });

  it('starts again at page 1 when a filter changes, and keeps the sort', async () => {
    const { router } = setup(manyAlerts(30), '/o/acme/alerts?sort=host&order=desc&page=2');
    await screen.findByText('26–30 of 30 alerts');

    const filters: Array<[string, string, string]> = [
      ['State', 'open', 'state'],
      ['Severity', 'crit', 'severity'],
      ['Scope', 'host', 'scope'],
      ['Acknowledged', 'true', 'acknowledged'],
      ['Muted', 'false', 'muted'],
    ];
    for (const [label, value, param] of filters) {
      await pickOption(screen, label, { value });
      await waitFor(() => expect(requests.at(-1)?.get(param)).toBe(value));
      // No request carried the new filter on the old page, and the sort stayed.
      expect(requests.filter((r) => r.get(param) === value).every((r) => r.get('page') === '1')).toBe(true);
      expect(requests.at(-1)?.get('sort')).toBe('host');
    }
    expect(router.state.location.search).not.toHaveProperty('page');
  });

  it('starts again at page 1 when the host scope is cleared, keeping the sort', async () => {
    const user = userEvent.setup();
    const { router } = setup(manyAlerts(30), '/o/acme/alerts?host=h1&sort=host&order=asc&page=2');
    await screen.findByText(/one host's alerts only/i);
    await user.click(screen.getByRole('button', { name: /show every alert/i }));
    await waitFor(() => expect(requests.at(-1)?.get('host')).toBeNull());
    expect(requests.at(-1)?.get('page')).toBe('1');
    expect(requests.at(-1)?.get('sort')).toBe('host');
    expect(router.state.location.search).toEqual({ sort: 'host', order: 'asc', host: undefined });
  });

  it('keeps the muted filter in the request', async () => {
    setup(manyAlerts(3), '/o/acme/alerts');
    await screen.findByText('host-01');
    await pickOption(screen, 'Muted', { value: 'true' });
    await waitFor(() => expect(requests.at(-1)?.get('muted')).toBe('true'));
  });

  it('shows the columns the list offers and hides one the person turned off', async () => {
    requests = [];
    mockApi({
      'GET /api/v1/auth/me/preferences': { ...PREFS, lists: { alerts: { columns: ['alert', 'open'] } } },
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/alerts': (req: MockRequest) => pageOf(manyAlerts(2), req),
    });
    renderPage();
    await screen.findByText('host-01');
    expect(screen.getByRole('columnheader', { name: /^Open/ })).toBeInTheDocument();
    expect(screen.queryByRole('columnheader', { name: /^Peak/ })).not.toBeInTheDocument();
  });

  it('says so in German', async () => {
    setup(manyAlerts(30), '/o/acme/alerts', 'de');
    expect(await screen.findByText('1–25 von 30 Alarme')).toBeInTheDocument();
    await screen.findByRole('button', { name: /^Quittierung/ });
  });
});
