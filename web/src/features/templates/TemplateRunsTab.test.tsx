import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { job, pageRoutes, renderTemplatePage, schedule, trigger } from './templatePageTestKit.tsx';

const RUNS = '/o/acme/templates/t1?tab=runs';
const runRow = async (id: string) => {
  const link = await screen.findByRole('link', { name: id.slice(0, 8) });
  return within(link.closest('[role="row"]') as HTMLElement);
};

describe('TemplateRunsTab', () => {
  it("lists the template's runs with start, what started them, commit, hosts and result", async () => {
    mockApi(
      pageRoutes({
        over: {
          'GET /api/v1/orgs/acme/schedules': { schedules: [schedule('s1')] },
          'GET /api/v1/orgs/acme/triggers': { triggers: [trigger('w1')] },
          'GET /api/v1/orgs/acme/jobs': {
            jobs: [
              job('job-aaaa1', { trigger: 'schedule', trigger_ref: 's1', host_count: 3, status: 'failed' }),
              job('job-bbbb2', { trigger: 'webhook', trigger_ref: 'w1', host_count: 1, pinned_commit: '0123456789abcdef0123', check_mode: true }),
              job('job-cccc3', { trigger: 'manual' }),
            ],
            total: 3,
          },
        },
      }),
    );
    renderTemplatePage(RUNS);
    const scheduled = await runRow('job-aaaa1');
    expect(await scheduled.findByText('Schedule · nightly-s1')).toBeInTheDocument();
    expect(scheduled.getByText('3 hosts')).toBeInTheDocument();
    expect(scheduled.getByText('failed')).toBeInTheDocument();
    expect(scheduled.getByText(/ago$/)).toBeInTheDocument();
    expect(scheduled.getByRole('link', { name: 'job-aaaa' })).toHaveAttribute('href', '/o/acme/runs/job-aaaa1');

    const hooked = await runRow('job-bbbb2');
    expect(await hooked.findByText('Webhook · push-w1')).toBeInTheDocument();
    expect(hooked.getByText('0123456789ab')).toBeInTheDocument();
    expect(hooked.getByText('1 host')).toBeInTheDocument();
    expect(hooked.getByText('Dry run')).toBeInTheDocument();

    const manual = await runRow('job-cccc3');
    expect(manual.getByText('Manual')).toBeInTheDocument();
  });

  it("asks the server for this template's runs, a page at a time", async () => {
    const user = userEvent.setup();
    const asked: URLSearchParams[] = [];
    mockApi(
      pageRoutes({
        over: {
          'GET /api/v1/orgs/acme/jobs': (req: MockRequest) => {
            asked.push(req.search);
            const page = Number(req.search.get('page') ?? '1');
            return { jobs: [job(`job-page${page}`)], total: 30 };
          },
        },
      }),
    );
    renderTemplatePage(RUNS);
    await runRow('job-page1');
    expect(asked.every((q) => q.get('template_id') === 't1')).toBe(true);
    await user.click(await screen.findByRole('button', { name: 'Next page' }));
    await runRow('job-page2');
    expect(asked.at(-1)?.get('page')).toBe('2');
    expect(asked.at(-1)?.get('template_id')).toBe('t1');
  });

  it('says when the template has not run yet', async () => {
    mockApi(pageRoutes({ over: { 'GET /api/v1/orgs/acme/jobs': { jobs: [], total: 0 } } }));
    renderTemplatePage(RUNS);
    expect(await within(await screen.findByRole('tabpanel')).findByText('This template has not run yet.')).toBeInTheDocument();
  });

  it('cancels a live run from its menu, which a viewer does not get', async () => {
    const user = userEvent.setup();
    let cancelled = '';
    mockApi(
      pageRoutes({
        over: {
          'GET /api/v1/orgs/acme/jobs': { jobs: [job('job-live1', { status: 'running' })], total: 1 },
          'POST /api/v1/orgs/acme/jobs/job-live1/cancel': (req: MockRequest) => ((cancelled = req.path), { job: job('job-live1', { status: 'cancelling' }) }),
        },
      }),
    );
    renderTemplatePage(RUNS);
    const r = await runRow('job-live1');
    await user.click(r.getByRole('button', { name: /actions/i }));
    await user.click(screen.getByRole('menuitem', { name: 'Cancel run' }));
    await waitFor(() => expect(cancelled).toBe('/api/v1/orgs/acme/jobs/job-live1/cancel'));
  });

  it('offers a viewer only Open in a live run’s menu', async () => {
    const user = userEvent.setup();
    mockApi(pageRoutes({ role: 'viewer', over: { 'GET /api/v1/orgs/acme/jobs': { jobs: [job('job-live1', { status: 'running' })], total: 1 } } }));
    renderTemplatePage(RUNS);
    const r = await runRow('job-live1');
    await user.click(r.getByRole('button', { name: /actions/i }));
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Open']);
  });
});
