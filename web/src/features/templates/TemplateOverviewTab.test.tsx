import { describe, expect, it } from 'vitest';
import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi } from '../../test/mockApi.ts';
import { job, pageRoutes, renderTemplatePage, runRequest, schedule, template, trigger } from './templatePageTestKit.tsx';

const region = async (name: string) => within(await screen.findByRole('region', { name }));

describe('TemplateOverviewTab', () => {
  it("lists the template's recent runs, each linked, and leads to the Runs tab", async () => {
    const user = userEvent.setup();
    let asked: URLSearchParams | undefined;
    mockApi(
      pageRoutes({
        over: {
          'GET /api/v1/orgs/acme/jobs': (req: { search: URLSearchParams }) => {
            asked = req.search;
            return { jobs: [job('aaaaaaaa-1111'), job('bbbbbbbb-2222', { status: 'failed', trigger: 'schedule', check_mode: true })], next_cursor: '' };
          },
        },
      }),
    );
    const { router } = renderTemplatePage();
    const runs = await region('Recent runs');
    const first = await runs.findByRole('link', { name: 'aaaaaaaa' });
    expect(first).toHaveAttribute('href', '/o/acme/runs/aaaaaaaa-1111');
    const second = runs.getByRole('link', { name: 'bbbbbbbb' });
    expect(second).toHaveTextContent('Schedule');
    expect(second).toHaveTextContent('Dry run');
    expect(second).toHaveTextContent('failed');
    expect(asked?.get('template_id')).toBe('t1');
    expect(asked?.get('limit')).toBe('5');

    await user.click(runs.getByRole('button', { name: 'All runs' }));
    expect(router.state.location.search).toEqual({ tab: 'runs' });
  });

  it('says when the template has not run yet', async () => {
    mockApi(pageRoutes());
    renderTemplatePage();
    expect(await (await region('Recent runs')).findByText('This template has not run yet.')).toBeInTheDocument();
  });

  it('shows its schedules, webhook triggers and follow-ups', async () => {
    const user = userEvent.setup();
    mockApi(
      pageRoutes({
        tpl: template({
          follow_ups: [
            { template_id: 't3', template_name: 'cleanup', position: 1 },
            { template_id: 't2', template_name: 'notify', position: 0 },
          ],
        }),
        over: {
          'GET /api/v1/orgs/acme/schedules': {
            schedules: [schedule('s1'), schedule('s2', { paused: true }), schedule('s3', { paused: true, paused_reason: 'assistant_change', review_request_id: 'rv1' })],
          },
          'GET /api/v1/orgs/acme/triggers': { triggers: [trigger('w1'), trigger('w2', { enabled: false })] },
        },
      }),
    );
    const { router } = renderTemplatePage();
    const starts = await region('What it starts');

    const s1 = await starts.findByRole('link', { name: 'nightly-s1' });
    expect(s1).toHaveAttribute('href', '/o/acme/schedules?id=s1&template=t1');
    expect(s1).toHaveTextContent('0 3 * * *');
    expect(s1).toHaveTextContent('active');
    expect(s1).toHaveTextContent('in 2 h');
    expect(starts.getByRole('link', { name: 'nightly-s2' })).toHaveTextContent('paused');
    expect(starts.getByRole('link', { name: 'nightly-s3' })).toHaveTextContent('needs review');

    const triggers = within(starts.getByRole('list', { name: 'Webhook triggers' }));
    expect(triggers.getAllByRole('listitem').map((li) => li.textContent)).toEqual(['push-w1enabled', 'push-w2disabled']);

    const followUps = starts.getAllByRole('link').filter((a) => a.getAttribute('href')?.startsWith('/o/acme/templates/'));
    expect(followUps.map((a) => a.getAttribute('href'))).toEqual(['/o/acme/templates/t2', '/o/acme/templates/t3']);
    // In the order they start: by position, not as stored.
    expect(starts.getByRole('link', { name: 'notify' })).toBe(followUps[0]);
    expect(followUps[1]).toHaveTextContent('2.');

    await user.click(starts.getByRole('button', { name: 'Triggers tab' }));
    expect(router.state.location.search).toEqual({ tab: 'triggers' });
  });

  it('says what starts nothing', async () => {
    mockApi(pageRoutes());
    renderTemplatePage();
    const starts = await region('What it starts');
    expect(await starts.findByText('No schedule runs this template.')).toBeInTheDocument();
    expect(await starts.findByText('No webhook trigger starts this template.')).toBeInTheDocument();
    expect(starts.getByText('A successful run starts nothing else.')).toBeInTheDocument();
  });

  it("lists this template's open run requests only, each linked to its request", async () => {
    mockApi(
      pageRoutes({
        over: {
          'GET /api/v1/orgs/acme/run-requests': {
            requests: [
              runRequest('q1'),
              runRequest('q2', { template_id: 't9' }),
              runRequest('q3', { status: 'approved' }),
              runRequest('q4', { status: 'waiting_for_sync', kind: 'schedule_review' }),
            ],
          },
        },
      }),
    );
    renderTemplatePage();
    const requests = await region('Open run requests');
    const links = await requests.findAllByRole('link', { name: /^Request/ });
    expect(links.map((a) => a.getAttribute('href'))).toEqual(['/o/acme/runs/requests/q1', '/o/acme/runs/requests/q4']);
    expect(links[0]).toHaveTextContent('pending');
    expect(requests.getByRole('link', { name: 'All requests' })).toHaveAttribute('href', '/o/acme/runs/requests?template=t1');
  });

  it('says when no request waits', async () => {
    mockApi(pageRoutes());
    renderTemplatePage();
    expect(await (await region('Open run requests')).findByText('No request for this template waits.')).toBeInTheDocument();
  });
});
