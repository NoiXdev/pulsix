import { describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import type { CalendarEntry } from '../../api/types.ts';
import { ScheduleCalendar } from './ScheduleCalendar.tsx';
import { addDays, entryTone, monthOf, rangeFor, shift, startOfWeek, weekCells, windowsIn } from './calendar.ts';

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
      <RouterProvider router={router} />
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
