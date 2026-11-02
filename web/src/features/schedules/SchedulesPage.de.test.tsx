import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { SchedulesPage } from './SchedulesPage.tsx';
import { relative } from './simple.ts';
import { de } from '../../i18n/de/index.ts';

const ORG = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } };
const template = { id: 't1', name: 'patch-all', playbook_id: 'pb1', scope: { group_id: null, host_ids: [] }, limit: '', extra_vars: {}, created_at: '', updated_at: '' };
const schedule = (over: Record<string, unknown> = {}) => ({
  id: 's1', template_id: 't1', name: 'Nightly OS patching', mode: 'cron', cron: '0 3 * * 2,4', description: 'Di, Do um 03:00',
  timezone: 'Pacific/Auckland', guards: { overlap: 'skip', catch_up: { enabled: false, window: '1h' } }, paused: false,
  next_run_at: '2026-09-22T01:00:00Z', revision: 3, created_at: '', updated_at: '',
  last_run: { id: 'r1', due_at: '', decided_at: '', outcome: 'triggered' }, notify_on: ['failure'], ...over,
});

function renderPage(path: string) {
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
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([listRoute])]),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <I18nProvider locale="de">
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe('SchedulesPage in German', () => {
  it('counts schedules in German and writes the editor preview in the schedule\'s own zone', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/job-templates': { templates: [template] },
      'GET /api/v1/orgs/acme/schedules': { schedules: [schedule(), schedule({ id: 's2', name: 'DB monitor user', paused: true, next_run_at: null, notify_on: [] })] },
      'GET /api/v1/orgs/acme/schedules/s1': { schedule: schedule() },
      'GET /api/v1/orgs/acme/schedules/s1/runs': {
        runs: [{ id: 'r1', due_at: '2026-09-15T01:00:00Z', decided_at: '2026-09-15T01:00:02Z', outcome: 'skipped_overlap', reason: 'a run of this template is still active' }],
      },
      'POST /api/v1/orgs/acme/schedules/preview': { cron: '0 3 * * 2,4', description: 'Di, Do um 03:00', next: ['2026-07-06T20:00:00Z'] },
    });
    renderPage('/o/acme/schedules?id=s1');

    expect(await screen.findByRole('heading', { name: 'Zeitpläne' })).toBeInTheDocument();
    expect(screen.getByText('2 Zeitpläne · aktiv: 1')).toBeInTheDocument();
    const paused = screen.getByText('DB monitor user').closest('[data-row]') as HTMLElement;
    expect(within(paused).getByText('pausiert')).toBeInTheDocument();

    const panel = await screen.findByRole('region', { name: 'Zeitplan-Editor' });
    expect(within(panel).getByRole('heading', { name: 'Zeitplan · Nightly OS patching' })).toBeInTheDocument();
    expect(await within(panel).findByText('übersprungen (Überlappung)')).toBeInTheDocument();
    // 20:00 UTC on 6 July is 08:00 on 7 July in Auckland, whatever zone the
    // browser sits in, and it is written in the German date format.
    expect(await within(panel).findByRole('listitem', { name: 'Ausführung 1' })).toHaveTextContent('07.07.2026, 08:00');
  });

  it('writes the next run and the count of one schedule in German', () => {
    const now = new Date('2026-09-18T10:00:00Z');
    expect(relative('2026-09-18T10:07:00Z', now, de)).toBe('in 7 min');
    expect(de.schedules.summary(1, 1)).toBe('1 Zeitplan · aktiv: 1');
    expect(de.schedules.pausedCount(1)).toBe('1 Zeitplan pausiert');
    expect(de.schedules.pausedCount(4)).toBe('4 Zeitpläne pausiert');
  });
});
