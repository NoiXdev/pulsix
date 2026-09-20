import { afterEach, describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import type { CalendarEntry } from '../../api/types.ts';
import { ScheduleCalendar } from './ScheduleCalendar.tsx';
import { MAX_RANGE_MS, addDays, entryTone, monthOf, rangeFor, shift, startOfWeek, weekCells, windowsIn } from './calendar.ts';

const monday = startOfWeek(new Date());
// Local times this week, so the test holds in every time zone.
const at = (day: number, hour: number) => new Date(monday.getFullYear(), monday.getMonth(), monday.getDate() + day, hour).toISOString();

const entry = (over: Partial<CalendarEntry>): CalendarEntry => ({
  schedule_id: 's1', schedule_name: 'Nightly OS patching', template_id: 't1', at: at(1, 3), state: 'planned', ...over,
});

function renderIt() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const page = createRoute({
    getParentRoute: () => orgRoute, path: '/schedules', component: () => <ScheduleCalendar org="acme" />,
    validateSearch: (s: Record<string, unknown>) => ({ id: typeof s.id === 'string' ? s.id : undefined, template: undefined }),
  });
  const run = createRoute({ getParentRoute: () => orgRoute, path: '/runs/$id', component: () => <div>RUN VIEW</div> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([page, run])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/schedules'] }),
  });
  render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
  return router;
}

describe('ScheduleCalendar', () => {
  it('draws the week with occurrences, counts and maintenance windows', async () => {
    let asked: URLSearchParams | undefined;
    mockApi({
      'GET /api/v1/orgs/acme/calendar': (req: MockRequest) => {
        asked = req.search;
        return {
          from: '', to: '', truncated: false,
          entries: [
            entry({ at: at(1, 3), state: 'triggered', job_id: 'j1', job_status: 'failed' }),
            entry({ schedule_id: 's2', schedule_name: 'Log rotation', at: at(2, 0) }),
            entry({ schedule_id: 's2', schedule_name: 'Log rotation', at: at(2, 1) }),
          ],
          maintenance: [{ window_id: 'w1', name: 'PostgreSQL', target_kind: 'org', start: at(5, 3), end: at(5, 5) }],
        };
      },
    });
    renderIt();
    const cal = await screen.findByRole('region', { name: 'Calendar' });
    expect(await within(cal).findByText('Nightly OS patching')).toBeInTheDocument();
    expect(within(cal).getByText('Log rotation ×2')).toBeInTheDocument();
    expect(within(cal).getByText('Maintenance · PostgreSQL')).toBeInTheDocument();
    expect(new Date(asked!.get('to')!).getTime() - new Date(asked!.get('from')!).getTime()).toBe(addDays(monday, 7).getTime() - monday.getTime());
  });

  it('opens the run of a past occurrence and switches to the list', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme/calendar': {
        from: '', to: '', truncated: true,
        entries: [entry({ state: 'triggered', job_id: 'j1', job_status: 'succeeded' }), entry({ schedule_name: 'Later', schedule_id: 's3', at: at(4, 9) })],
        maintenance: [{ window_id: 'w1', name: 'PostgreSQL', target_kind: 'org', start: at(5, 3), end: at(5, 5) }],
      },
    });
    const router = renderIt();
    const cal = await screen.findByRole('region', { name: 'Calendar' });
    expect(await within(cal).findByText('Some very frequent schedules are shown in part.')).toBeInTheDocument();
    await user.click(within(cal).getByRole('tab', { name: 'list' }));
    const rows = await within(cal).findAllByText((_, el) => el?.hasAttribute('data-list-row') ?? false);
    expect(rows).toHaveLength(3);
    expect(within(cal).getByText('succeeded')).toBeInTheDocument();
    await user.click(within(cal).getByRole('button', { name: 'Later' }));
    await waitFor(() => expect(router.state.location.search).toMatchObject({ id: 's3' }));
  });

  it('navigates to the run view from the week', async () => {
    const user = userEvent.setup();
    mockApi({ 'GET /api/v1/orgs/acme/calendar': { from: '', to: '', truncated: false, entries: [entry({ state: 'triggered', job_id: 'j1', job_status: 'failed' })], maintenance: [] } });
    renderIt();
    await user.click(await screen.findByRole('button', { name: 'Nightly OS patching' }));
    expect(await screen.findByText('RUN VIEW')).toBeInTheDocument();
  });

  it('shows six weeks in the month view', async () => {
    const user = userEvent.setup();
    mockApi({ 'GET /api/v1/orgs/acme/calendar': { from: '', to: '', truncated: false, entries: [entry({})], maintenance: [] } });
    renderIt();
    const cal = await screen.findByRole('region', { name: 'Calendar' });
    await user.click(within(cal).getByRole('tab', { name: 'month' }));
    await waitFor(() => expect(cal.querySelectorAll('[data-day]')).toHaveLength(42));
  });
});

/**
 * Final review, Critical #1 (second half): ScheduleCalendar read
 * `cal.data?.entries ?? []` and used no useQueryGuard, so a refused
 * request rendered as a complete, empty grid — no error, no toast, no
 * retry hint. That is what turned a range-arithmetic slip into something
 * shaped like data loss, and it would do the same for any other calendar
 * failure (500, 403, network).
 */
describe('a failed calendar query', () => {
  it('reports the failure instead of drawing an empty grid', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/calendar': () =>
        new Response(JSON.stringify({ detail: 'invalid range: to must be after from, at most 6 weeks later' }), {
          status: 400,
          headers: { 'Content-Type': 'application/json' },
        }),
    });
    renderIt();
    expect(await screen.findByText('Something went wrong')).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'Calendar' })).toBeNull();
  });
});

describe('calendar helpers', () => {
  it('computes weeks, ranges and tones', () => {
    const wed = new Date(2026, 8, 23, 15, 30);
    expect(startOfWeek(wed)).toEqual(new Date(2026, 8, 21));
    const { from, to } = rangeFor('month', new Date(2026, 8, 21));
    expect(from).toEqual(new Date(2026, 7, 31));
    expect(addDays(from, 42)).toEqual(to);
    expect(shift('week', new Date(2026, 8, 21), 1)).toEqual(new Date(2026, 8, 28));
    expect(shift('month', new Date(2026, 8, 21), 1)).toEqual(new Date(2026, 9, 5));
    expect(shift('month', new Date(2026, 8, 21), -1)).toEqual(new Date(2026, 7, 3));
    // The week of 31 Aug 2026 belongs to September (its Thursday is 3 Sep).
    expect(monthOf(new Date(2026, 7, 31))).toEqual(new Date(2026, 8, 1));
    expect(entryTone(entry({}))).toBe('unknown');
    expect(entryTone(entry({ state: 'triggered', job_status: 'running' }))).toBe('accent');
    expect(entryTone(entry({ state: 'triggered', job_status: 'cancelled' }))).toBe('warn');
    expect(entryTone(entry({ state: 'failed' }))).toBe('crit');
    expect(entryTone(entry({ state: 'missed' }))).toBe('warn');
    const cells = weekCells([entry({ at: new Date(2026, 8, 22, 4).toISOString() }), entry({ at: new Date(2026, 8, 29).toISOString() })], new Date(2026, 8, 21));
    expect(cells[1][1]).toHaveLength(1);
    expect(cells.flat(2)).toHaveLength(1);
    expect(windowsIn([{ window_id: 'w', name: 'x', target_kind: 'org', start: new Date(2026, 8, 22, 2).toISOString(), end: new Date(2026, 8, 22, 4).toISOString() }], new Date(2026, 8, 22, 3), new Date(2026, 8, 22, 6))).toHaveLength(1);
  });
});

/**
 * Review finding (Important, fix round 1): weekCells used to bucket a day
 * index with Math.floor(msDiff / 86_400_000), which assumes every day in
 * the grid is exactly 24h. A DST-changing week breaks that assumption:
 * Europe/Berlin's 2026 spring-forward (Sunday 29 March, clocks 02:00 →
 * 03:00, a 23h day) or fall-back (Sunday 25 October, clocks 03:00 → 02:00,
 * a 25h day) changes real elapsed time without changing the number of
 * calendar days crossed. Both fixtures below start the grid on a Thursday
 * (not a Monday) so the transition day falls *before* the target within
 * the 7-day window — the one condition under which the old ms-division
 * arithmetic actually misplaces the entry (a Monday-start week, the only
 * shape the app itself ever builds, happens to dodge it because the
 * transition always lands on the grid's last day, a Sunday).
 */
describe('weekCells across a DST transition', () => {
  // The project's tsconfig carries no Node types (no @types/node dependency
  // — adding one is out of scope for a test-only need), but Vitest itself
  // runs on Node, where the global `process` exists at runtime; reading it
  // through globalThis sidesteps the missing ambient type the same way
  // src/test/setup.ts reaches other runtime globals it isn't typed for.
  const nodeEnv = (globalThis as unknown as { process: { env: Record<string, string | undefined> } }).process.env;
  const originalTZ = nodeEnv.TZ;
  afterEach(() => {
    if (originalTZ === undefined) delete nodeEnv.TZ;
    else nodeEnv.TZ = originalTZ;
  });

  it('places an occurrence after a spring-forward (23h) day in its correct column, not one early', () => {
    nodeEnv.TZ = 'Europe/Berlin';
    const start = new Date(2026, 2, 26); // Thursday 26 March 2026, local midnight
    const target = new Date(2026, 2, 30, 9); // Monday 30 March 2026, 09:00 local (index 4)
    const cells = weekCells([entry({ at: target.toISOString() })], start);
    expect(cells[4].flat()).toHaveLength(1); // Monday, the correct column
    expect(cells[3].flat()).toHaveLength(0); // Sunday (the transition day) — not here
  });

  it('places an occurrence after a fall-back (25h) day in its correct column', () => {
    nodeEnv.TZ = 'Europe/Berlin';
    const start = new Date(2026, 9, 22); // Thursday 22 October 2026, local midnight
    const target = new Date(2026, 9, 26, 9); // Monday 26 October 2026, 09:00 local (index 4)
    const cells = weekCells([entry({ at: target.toISOString() })], start);
    expect(cells[4].flat()).toHaveLength(1); // Monday, the correct column
    expect(cells[3].flat()).toHaveLength(0); // Sunday (the transition day) — not here
  });
});

/**
 * Final review, Critical #1: rangeFor('month', ...) builds the six week
 * rows with setDate, so its span is 42 *calendar* days — 1009 h across an
 * autumn fall-back, one hour past the server's old absolute 42x24 h bound.
 * The request was refused with 400 and ScheduleCalendar drew the refusal
 * as a fully rendered, completely empty grid. The helper test above anchors
 * on September, which contains no transition, which is exactly why this
 * shipped.
 *
 * The assertion is against MAX_RANGE_MS, the mirror of internal/calendar's
 * MaxRange, so the client can never ask for more than the server accepts.
 */
describe('rangeFor across an autumn DST transition', () => {
  // Reaching process.env through globalThis, as the weekCells DST block
  // above does: the project's tsconfig carries no Node types.
  const nodeEnv = (globalThis as unknown as { process: { env: Record<string, string | undefined> } }).process.env;
  const originalTZ = nodeEnv.TZ;
  afterEach(() => {
    if (originalTZ === undefined) delete nodeEnv.TZ;
    else nodeEnv.TZ = originalTZ;
  });

  const HOUR = 60 * 60 * 1000;
  // Each case is a month whose six-week grid contains a fall-back
  // transition: Berlin's on 25 October 2026, New York's on 1 November 2026
  // (which lands in both the October and the November grid).
  const cases: Array<[string, string, Date]> = [
    ['Europe/Berlin', 'October 2026', new Date(2026, 9, 5)],
    ['America/New_York', 'October 2026', new Date(2026, 9, 5)],
    ['America/New_York', 'November 2026', new Date(2026, 10, 9)],
  ];

  for (const [zone, month, anchor] of cases) {
    it(`asks for a range the server accepts in ${zone}, ${month}`, () => {
      nodeEnv.TZ = zone;
      const { from, to } = rangeFor('month', new Date(anchor));
      const span = to.getTime() - from.getTime();
      expect(span).toBe(1009 * HOUR); // 42 calendar days across the fall-back
      expect(span).toBeLessThanOrEqual(MAX_RANGE_MS);
      // The grid still covers the 42 day cells it draws.
      expect(addDays(from, 42)).toEqual(to);
    });
  }

  it('leaves a spring-forward month and UTC untouched', () => {
    nodeEnv.TZ = 'Europe/Berlin';
    const spring = rangeFor('month', new Date(2026, 2, 16)); // March 2026
    expect(spring.to.getTime() - spring.from.getTime()).toBe(1007 * HOUR);
    nodeEnv.TZ = 'UTC';
    const utc = rangeFor('month', new Date(2026, 9, 5));
    expect(utc.to.getTime() - utc.from.getTime()).toBe(1008 * HOUR);
  });
});
