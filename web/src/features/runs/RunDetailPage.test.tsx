import { afterEach, describe, expect, it } from 'vitest';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { RunDetailPage } from './RunDetailPage.tsx';

const ORG = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } };
const JOB_ID = '0197c0a2-0000-7000-8000-000000000001';

const event = (seq: number, state = 'ok', extra: Record<string, unknown> = {}) => ({
  seq, at: '2026-09-15T08:00:00Z', host: 'web-01', task: 'Install nginx', state,
  payload: extra.text !== undefined ? { text: extra.text } : { stdout: 'ok' },
});

const detail = (over: Record<string, unknown> = {}) => ({
  job: {
    id: JOB_ID, zone_id: '0197c0b1-0000-7000-8000-000000000002', kind: 'run', status: 'succeeded',
    template: { id: 't1', name: 'deploy-site' }, run_group_id: 'g1', trigger: 'manual',
    actor_name: 'Ada', worker_name: 'worker-fra', hosts: [], stats: {}, error: undefined,
    started_at: '2026-09-15T08:00:01Z', finished_at: '2026-09-15T08:00:09Z', created_at: '2026-09-15T08:00:00Z',
    group: [],
    ...over,
  },
});

/* A minimal EventSource fake: the hook subscribes with addEventListener and
 * the tests drive frames by hand. */
class FakeEventSource {
  static instances: FakeEventSource[] = [];
  static last(): FakeEventSource | undefined {
    return FakeEventSource.instances[FakeEventSource.instances.length - 1];
  }
  static reset(): void {
    FakeEventSource.instances = [];
  }
  url: string;
  closed = false;
  private listeners = new Map<string, ((e: { data: string }) => void)[]>();
  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }
  addEventListener(type: string, fn: (e: { data: string }) => void) {
    const list = this.listeners.get(type) ?? [];
    list.push(fn);
    this.listeners.set(type, list);
  }
  emit(type: string, data: unknown) {
    for (const fn of this.listeners.get(type) ?? []) act(() => fn({ data: JSON.stringify(data) }));
  }
  close() {
    this.closed = true;
  }
}

const originalEventSource = globalThis.EventSource;

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const listRoute = createRoute({ getParentRoute: () => orgRoute, path: '/runs', component: () => <div>RUNS LIST</div> });
  const detailRoute = createRoute({ getParentRoute: () => orgRoute, path: '/runs/$id', component: RunDetailPage });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([listRoute, detailRoute])]),
    history: createMemoryHistory({ initialEntries: [`/o/acme/runs/${JOB_ID}`] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  globalThis.EventSource = originalEventSource;
  FakeEventSource.reset();
});

describe('RunDetailPage', () => {
  it('renders the header facts, per-host results and event log of a finished run', async () => {
    globalThis.EventSource = FakeEventSource as unknown as typeof EventSource;
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/jobs/0197c0a2-0000-7000-8000-000000000001': {
        job: detail({
          stats: { ok_hosts: ['h1'], changed_hosts: ['h1'], failed_hosts: ['h2'], unreachable_hosts: [], skipped_hosts: ['h2'] },
          group: [{ id: '0197c0aa-0000-7000-8000-000000000009', zone_id: '0197c0b1-0000-7000-8000-000000000002', status: 'succeeded' }],
        }).job,
      },
      'GET /api/v1/orgs/acme/jobs/0197c0a2-0000-7000-8000-000000000001/events': { events: [event(1, 'task'), event(2), event(3, 'failed')] },
    });
    renderPage();

    expect(await screen.findByText('deploy-site')).toBeInTheDocument();
    expect(screen.getByText('succeeded')).toBeInTheDocument();
    expect(screen.getByText('Ada')).toBeInTheDocument();
    expect(screen.getByText('worker-fra')).toBeInTheDocument();
    const table = screen.getByRole('table', { name: 'Per-host results' });
    expect(within(table).getByText('h1')).toBeInTheDocument();
    expect(within(table).getAllByText('failed').length).toBeGreaterThan(0);
    // Siblings strip: the run group's other job is linked.
    expect(screen.getByText('Same trigger:')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: '0197c0aa' })).toBeInTheDocument();
    const log = screen.getByRole('list', { name: 'Run events' });
    expect(within(log).getAllByText('Install nginx').length).toBeGreaterThan(0);
  });

  it('shows the error banner of a failed run', async () => {
    globalThis.EventSource = FakeEventSource as unknown as typeof EventSource;
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/jobs/0197c0a2-0000-7000-8000-000000000001': { job: detail({ status: 'failed', error: '2 hosts failed' }).job },
      'GET /api/v1/orgs/acme/jobs/0197c0a2-0000-7000-8000-000000000001/events': { events: [] },
    });
    renderPage();
    expect(await screen.findByRole('alert')).toHaveTextContent('2 hosts failed');
  });

  it('enables retry-failed only when the run has failed hosts, and navigates to the new job', async () => {
    globalThis.EventSource = FakeEventSource as unknown as typeof EventSource;
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/jobs/0197c0a2-0000-7000-8000-000000000001': { job: detail({ status: 'failed', stats: { failed_hosts: ['h2'] } }).job },
      'GET /api/v1/orgs/acme/jobs/0197c0a2-0000-7000-8000-000000000001/events': { events: [] },
      'POST /api/v1/orgs/acme/jobs/0197c0a2-0000-7000-8000-000000000001/retry-failed': { jobs: [{ id: '0197c0bb-0000-7000-8000-00000000000c', zone_id: 'z', kind: 'run', status: 'queued', run_group_id: 'g2', trigger: 'manual', created_at: '' }] },
      'GET /api/v1/orgs/acme/jobs/0197c0bb-0000-7000-8000-00000000000c': { job: detail({ id: '0197c0bb-0000-7000-8000-00000000000c', status: 'queued', run_group_id: 'g2' }).job },
      'GET /api/v1/orgs/acme/jobs/0197c0bb-0000-7000-8000-00000000000c/events': { events: [] },
    });
    renderPage();
    const retry = await screen.findByRole('button', { name: 'Retry failed' });
    expect(retry).toBeEnabled();
    await user.click(retry);
    expect(await screen.findByText('0197c0bb')).toBeInTheDocument();
  });

  it('keeps retry-failed disabled for a run with no failed hosts', async () => {
    globalThis.EventSource = FakeEventSource as unknown as typeof EventSource;
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/jobs/0197c0a2-0000-7000-8000-000000000001': { job: detail({ status: 'succeeded', stats: { ok_hosts: ['h1'] } }).job },
      'GET /api/v1/orgs/acme/jobs/0197c0a2-0000-7000-8000-000000000001/events': { events: [] },
    });
    renderPage();
    expect(await screen.findByRole('button', { name: 'Retry failed' })).toBeDisabled();
  });

  it('offers cancel while the run is live and reports the outcome', async () => {
    globalThis.EventSource = FakeEventSource as unknown as typeof EventSource;
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/jobs/0197c0a2-0000-7000-8000-000000000001': { job: detail({ status: 'running' }).job },
      'GET /api/v1/orgs/acme/jobs/0197c0a2-0000-7000-8000-000000000001/events': { events: [] },
      'POST /api/v1/orgs/acme/jobs/0197c0a2-0000-7000-8000-000000000001/cancel': { job: { id: JOB_ID, status: 'cancelling' } },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Cancel run' }));
    expect(await screen.findByText('Cancellation requested')).toBeInTheDocument();
  });

  it('subscribes to the SSE stream, ends it on the terminal status frame', async () => {
    globalThis.EventSource = FakeEventSource as unknown as typeof EventSource;
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/jobs/0197c0a2-0000-7000-8000-000000000001': { job: detail({ status: 'running' }).job },
      'GET /api/v1/orgs/acme/jobs/0197c0a2-0000-7000-8000-000000000001/events': { events: [event(1, 'task')] },
    });
    renderPage();
    expect(await screen.findByRole('list', { name: 'Run events' })).toBeInTheDocument();
    const es = FakeEventSource.last();
    expect(es?.url).toBe('/api/v1/orgs/acme/jobs/0197c0a2-0000-7000-8000-000000000001/stream');
    expect(es?.closed).toBe(false);
    es?.emit('status', { id: JOB_ID, status: 'succeeded' });
    expect(es?.closed).toBe(true);
  });

  it('falls back to polling /events?after=<seq> when the stream drops frames (seq gap)', async () => {
    globalThis.EventSource = FakeEventSource as unknown as typeof EventSource;
    const user = userEvent.setup();
    const eventFetches: string[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/jobs/0197c0a2-0000-7000-8000-000000000001': { job: detail({ status: 'running' }).job },
      'GET /api/v1/orgs/acme/jobs/0197c0a2-0000-7000-8000-000000000001/events': (req: MockRequest) => {
        eventFetches.push(req.search.get('after') ?? '');
        const after = Number(req.search.get('after') ?? '0');
        return { events: after === 0 ? [event(1, 'task'), event(2)] : [event(3), event(4), event(5)] };
      },
    });
    renderPage();
    await screen.findByRole('list', { name: 'Run events' });
    const es = FakeEventSource.last();
    // seq 1 and 2 arrived through the seed; the stream's next frame is 5 —
    // frames 3 and 4 were dropped (drop-on-full) → the client must detect
    // the gap, close the stream and poll the history instead.
    es?.emit('log', event(5));
    expect(es?.closed).toBe(true);
    // The polled events fill the gap and render (state lines with host/task).
    const log = await screen.findByRole('list', { name: 'Run events' });
    await waitFor(() => expect(within(log).getAllByText('ok').length).toBeGreaterThanOrEqual(3));
    expect(eventFetches).toContain('2');
    void user;
  });
});

describe('RunDetailPage trigger', () => {
  it('names a scheduled run and links to its schedule', async () => {
    globalThis.EventSource = FakeEventSource as unknown as typeof EventSource;
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      [`GET /api/v1/orgs/acme/jobs/${JOB_ID}`]: detail({ trigger: 'schedule', trigger_ref: 's1', actor_name: undefined }),
      [`GET /api/v1/orgs/acme/jobs/${JOB_ID}/events`]: { events: [] },
    });
    renderPage();
    const link = await screen.findByRole('link', { name: 'Schedule' });
    expect(link.getAttribute('href')).toContain('/o/acme/schedules?id=s1');
  });

  it('names a webhook run', async () => {
    globalThis.EventSource = FakeEventSource as unknown as typeof EventSource;
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      [`GET /api/v1/orgs/acme/jobs/${JOB_ID}`]: detail({ trigger: 'webhook', trigger_ref: 'tr1', actor_name: undefined }),
      [`GET /api/v1/orgs/acme/jobs/${JOB_ID}/events`]: { events: [] },
    });
    renderPage();
    expect(await screen.findByText('Webhook')).toBeInTheDocument();
  });
});
