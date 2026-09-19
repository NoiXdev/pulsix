import { describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { SchedulesPage } from './SchedulesPage.tsx';

const ORG = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } };
const template = { id: 't1', name: 'patch-all', playbook_id: 'pb1', scope: { group_id: null, host_ids: [] }, limit: '', extra_vars: {}, created_at: '', updated_at: '' };
const schedule = (over: Record<string, unknown> = {}) => ({
  id: 's1', template_id: 't1', name: 'Nightly OS patching', mode: 'cron', cron: '0 3 * * 2,4', description: 'Tue, Thu at 03:00',
  timezone: 'Europe/Berlin', guards: { overlap: 'skip', catch_up: { enabled: false, window: '1h' } }, paused: false,
  next_run_at: '2026-09-22T01:00:00Z', revision: 3, created_at: '', updated_at: '',
  last_run: { id: 'r1', due_at: '', decided_at: '', outcome: 'triggered' }, ...over,
});

function renderPage(path = '/o/acme/schedules') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const listRoute = createRoute({
    getParentRoute: () => orgRoute, path: '/schedules', component: SchedulesPage,
    validateSearch: (s: Record<string, unknown>) => ({
      id: typeof s.id === 'string' ? s.id : undefined,
      template: typeof s.template === 'string' ? s.template : undefined,
    }),
  });
  const runRoute = createRoute({ getParentRoute: () => orgRoute, path: '/runs/$id', component: () => <div>RUN VIEW</div> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([listRoute, runRoute])]),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

const base = {
  'GET /api/v1/orgs/acme': ORG,
  'GET /api/v1/orgs/acme/job-templates': { templates: [template] },
};

describe('SchedulesPage', () => {
  it('lists schedules with template, cron, human text and last result', async () => {
    mockApi({ ...base, 'GET /api/v1/orgs/acme/schedules': { schedules: [schedule(), schedule({ id: 's2', name: 'DB monitor user', paused: true, next_run_at: null })] } });
    renderPage();
    const row = (await screen.findByText('Nightly OS patching')).closest('[data-row]') as HTMLElement;
    expect(within(row).getByText('0 3 * * 2,4')).toBeInTheDocument();
    expect(within(row).getByText('Tue, Thu at 03:00')).toBeInTheDocument();
    expect(within(row).getByText('patch-all')).toBeInTheDocument();
    expect(within(row).getByText('triggered')).toBeInTheDocument();
    const paused = (screen.getByText('DB monitor user')).closest('[data-row]') as HTMLElement;
    expect(within(paused).getByText('paused')).toBeInTheDocument();
  });

  it('creates a schedule in simple mode and shows the next five occurrences', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    let previewed: unknown;
    mockApi({
      ...base,
      'GET /api/v1/orgs/acme/schedules': { schedules: [] },
      'POST /api/v1/orgs/acme/schedules/preview': (req: MockRequest) => {
        previewed = req.body;
        return { cron: '0 3 * * 2,4', description: 'Tue, Thu at 03:00', next: ['2026-09-22T01:00:00Z', '2026-09-24T01:00:00Z', '2026-09-29T01:00:00Z', '2026-10-01T01:00:00Z', '2026-10-06T01:00:00Z'] };
      },
      'POST /api/v1/orgs/acme/schedules': (req: MockRequest) => {
        posted = req.body;
        return { schedule: schedule() };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New schedule' }));
    const panel = screen.getByRole('region', { name: 'Schedule editor' });
    await user.type(within(panel).getByLabelText('Name'), 'Nightly OS patching');
    await user.selectOptions(within(panel).getByLabelText('Job template'), 't1');
    await user.selectOptions(within(panel).getByLabelText('Repeat'), 'weekly');
    await user.click(within(panel).getByRole('checkbox', { name: 'Tue' }));
    await user.click(within(panel).getByRole('checkbox', { name: 'Thu' }));
    await user.click(within(panel).getByRole('checkbox', { name: 'Mon' })); // default day off again
    // The preview is debounced: wait for the request of the final state.
    await waitFor(() => expect(previewed).toMatchObject({ mode: 'simple', simple: { kind: 'weekly', days: ['tue', 'thu'], time: '03:00' } }));
    expect(await within(panel).findByText('Tue, Thu at 03:00')).toBeInTheDocument();
    expect(within(panel).getAllByRole('listitem', { name: /occurrence/ })).toHaveLength(5);
    await user.click(within(panel).getByRole('button', { name: 'Save' }));
    expect(posted).toMatchObject({
      template_id: 't1', name: 'Nightly OS patching', mode: 'simple', timezone: expect.any(String),
      simple: { kind: 'weekly', days: ['tue', 'thu'], time: '03:00' },
      guards: { overlap: 'skip', catch_up: { enabled: false, window: '1h' } },
    });
  });

  it('saves an edit with If-Match and runs now into the run view', async () => {
    const user = userEvent.setup();
    let ifMatch: string | null = null;
    mockApi({
      ...base,
      'GET /api/v1/orgs/acme/schedules': { schedules: [schedule()] },
      'GET /api/v1/orgs/acme/schedules/s1': { schedule: schedule() },
      'GET /api/v1/orgs/acme/schedules/s1/runs': { runs: [{ id: 'r1', due_at: '2026-09-15T01:00:00Z', decided_at: '2026-09-15T01:00:02Z', outcome: 'skipped_overlap', reason: 'a run of this template is still active' }] },
      'POST /api/v1/orgs/acme/schedules/preview': { cron: '0 3 * * 2,4', description: '', next: [] },
      'PUT /api/v1/orgs/acme/schedules/s1': (req: MockRequest) => {
        ifMatch = req.headers.get('If-Match');
        return { schedule: schedule({ revision: 4 }) };
      },
      'POST /api/v1/orgs/acme/schedules/s1/run': { jobs: [{ id: 'job-7', zone_id: 'z', kind: 'run', status: 'queued', run_group_id: 'g', trigger: 'schedule', created_at: '' }] },
    });
    renderPage('/o/acme/schedules?id=s1');
    const panel = await screen.findByRole('region', { name: 'Schedule editor' });
    expect(await within(panel).findByText('a run of this template is still active')).toBeInTheDocument();
    await user.click(within(panel).getByRole('button', { name: 'Save' }));
    await screen.findByText('Schedule saved');
    expect(ifMatch).toBe('"3"');
    await user.click(within(panel).getByRole('button', { name: 'Run now' }));
    expect(await screen.findByText('RUN VIEW')).toBeInTheDocument();
  });

  it('surfaces a toast instead of an unhandled rejection when delete fails', async () => {
    const user = userEvent.setup();
    window.confirm = () => true;
    mockApi({
      ...base,
      'GET /api/v1/orgs/acme/schedules': { schedules: [schedule()] },
      'GET /api/v1/orgs/acme/schedules/s1': { schedule: schedule() },
      'GET /api/v1/orgs/acme/schedules/s1/runs': { runs: [] },
      'POST /api/v1/orgs/acme/schedules/preview': { cron: '0 3 * * 2,4', description: '', next: [] },
      'DELETE /api/v1/orgs/acme/schedules/s1': () => new Response(JSON.stringify({ detail: 'schedule still referenced' }), { status: 409 }),
    });
    renderPage('/o/acme/schedules?id=s1');
    const panel = await screen.findByRole('region', { name: 'Schedule editor' });
    await user.click(await within(panel).findByRole('button', { name: 'Delete' }));
    const toast = await screen.findByRole('status');
    expect(toast).toHaveTextContent('schedule still referenced');
    // The panel stays open — a failed delete must not act as if it succeeded.
    expect(screen.getByRole('region', { name: 'Schedule editor' })).toBeInTheDocument();
  });

  it('surfaces a toast instead of an unhandled rejection when pause fails', async () => {
    const user = userEvent.setup();
    mockApi({
      ...base,
      'GET /api/v1/orgs/acme/schedules': { schedules: [schedule()] },
      'GET /api/v1/orgs/acme/schedules/s1': { schedule: schedule() },
      'GET /api/v1/orgs/acme/schedules/s1/runs': { runs: [] },
      'POST /api/v1/orgs/acme/schedules/preview': { cron: '0 3 * * 2,4', description: '', next: [] },
      'POST /api/v1/orgs/acme/schedules/s1/pause': () => new Response(JSON.stringify({ detail: 'schedule already paused' }), { status: 409 }),
    });
    renderPage('/o/acme/schedules?id=s1');
    const panel = await screen.findByRole('region', { name: 'Schedule editor' });
    await user.click(await within(panel).findByRole('button', { name: 'Pause' }));
    const toast = await screen.findByRole('status');
    expect(toast).toHaveTextContent('schedule already paused');
  });

  it('pauses all schedules after confirmation and hides writes from viewers', async () => {
    const user = userEvent.setup();
    let paused = false;
    mockApi({
      ...base,
      'GET /api/v1/orgs/acme/schedules': { schedules: [schedule()] },
      'POST /api/v1/orgs/acme/schedules/pause-all': () => {
        paused = true;
        return { paused: 1 };
      },
    });
    window.confirm = () => true;
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Pause all' }));
    expect(paused).toBe(true);
  });

  it('offers no write buttons to a viewer', async () => {
    mockApi({ ...base, 'GET /api/v1/orgs/acme': { org: { ...ORG.org, role: 'viewer' } }, 'GET /api/v1/orgs/acme/schedules': { schedules: [schedule()] } });
    renderPage();
    expect(await screen.findByText('Nightly OS patching')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'New schedule' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Pause all' })).not.toBeInTheDocument();
  });

  it('sets the maintenance guard and shows skipped_maintenance runs', async () => {
    const user = userEvent.setup();
    let put: unknown;
    mockApi({
      ...base,
      'GET /api/v1/orgs/acme/schedules': { schedules: [schedule()] },
      'GET /api/v1/orgs/acme/schedules/s1': { schedule: schedule({ guards: { overlap: 'skip', catch_up: { enabled: false, window: '1h' }, maintenance: 'skip' } }) },
      'GET /api/v1/orgs/acme/schedules/s1/runs': { runs: [{ id: 'r1', due_at: '2026-09-15T01:00:00Z', decided_at: '2026-09-15T01:00:02Z', outcome: 'skipped_maintenance', reason: '1 targeted host(s) in a maintenance window' }] },
      'POST /api/v1/orgs/acme/schedules/preview': { cron: '0 3 * * 2,4', description: '', next: [] },
      'PUT /api/v1/orgs/acme/schedules/s1': (req: MockRequest) => {
        put = req.body;
        return { schedule: schedule() };
      },
    });
    renderPage('/o/acme/schedules?id=s1');
    const panel = await screen.findByRole('region', { name: 'Schedule editor' });
    expect(await within(panel).findByText('skipped_maintenance')).toBeInTheDocument();
    const select = within(panel).getByLabelText('When a targeted host is in a maintenance window');
    expect(select).toHaveValue('skip');
    await user.selectOptions(select, 'exclude_hosts');
    await user.click(within(panel).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(put).toMatchObject({ guards: { maintenance: 'exclude_hosts' } }));
  });
});
