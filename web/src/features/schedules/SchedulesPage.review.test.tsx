import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { SchedulesPage } from './SchedulesPage.tsx';

const REVIEW = '0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b';
const ORG = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } };
const template = { id: 't1', name: 'patch-all', playbook_id: 'pb1', scope: { group_id: null, host_ids: [] }, limit: '', extra_vars: {}, created_at: '', updated_at: '' };
const paused = {
  id: 's1', template_id: 't1', name: 'Nightly OS patching', mode: 'cron', cron: '0 3 * * 2,4', description: 'Tue, Thu at 03:00',
  timezone: 'Europe/Berlin', guards: { overlap: 'skip', catch_up: { enabled: false, window: '1h' } }, paused: true,
  paused_reason: 'assistant_change', review_request_id: REVIEW, next_run_at: null, revision: 3, created_at: '', updated_at: '',
};

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
  const router = createRouter({ routeTree: rootRoute.addChildren([orgRoute.addChildren([listRoute])]), history: createMemoryHistory({ initialEntries: [path] }) });
  return render(
    <QueryClientProvider client={qc}>
      <ConfirmProvider>
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </ConfirmProvider>
    </QueryClientProvider>,
  );
}

function mockSchedules() {
  mockApi({
    'GET /api/v1/orgs/acme': ORG,
    'GET /api/v1/orgs/acme/job-templates': { templates: [template] },
    'GET /api/v1/orgs/acme/schedules': { schedules: [paused] },
    'GET /api/v1/orgs/acme/schedules/s1': { schedule: paused },
    'GET /api/v1/orgs/acme/schedules/s1/runs': { runs: [] },
    'GET /api/v1/orgs/acme/schedules/s1/notifications': { rules: [] },
    'POST /api/v1/orgs/acme/schedules/preview': { cron: '0 3 * * 2,4', description: '', next: [] },
  });
}

describe('SchedulesPage: a schedule an assistant change paused (spec 2026-11-25 C2)', () => {
  it('marks it in the list as needing review', async () => {
    mockSchedules();
    renderPage('/o/acme/schedules');
    const row = (await screen.findByText('Nightly OS patching')).closest('[role="row"]') as HTMLElement;
    expect(within(row).getByText('needs review')).toBeInTheDocument();
  });

  it('says why in the editor and links the review request', async () => {
    mockSchedules();
    renderPage('/o/acme/schedules?id=s1');
    const panel = await screen.findByRole('region', { name: 'Schedule editor' });
    const note = await within(panel).findByRole('note', { name: 'Paused: changed by an assistant, needs review' });
    expect(within(note).getByRole('link', { name: 'Open the review' })).toHaveAttribute('href', `/o/acme/runs/requests/${REVIEW}`);
  });
});
