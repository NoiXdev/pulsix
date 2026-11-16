import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRoute } from '../../test/mockApi.ts';
import { acceptConfirm, declineConfirm } from '../../test/confirmDialog.ts';
import { fetchMock } from '../../test/fetchMock.ts';
import { FakeEventSource, renderRunPage } from './runPageTestKit.tsx';

const JOB_ID = '0197c0a2-0000-7000-8000-000000000001';
const CHILD_ID = '0197c0cc-0000-7000-8000-000000000003';
const PARENT_ID = '0197c0dd-0000-7000-8000-000000000004';
const JOB_PATH = `/api/v1/orgs/acme/jobs/${JOB_ID}`;

const org = (role = 'operator') => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });

const followUp = (over: Record<string, unknown>) => ({
  id: 'f1', template_id: 't2', template_name: 'reload-cache', state: 'pending', reason: '', hosts: ['h1', 'h2'], depth: 1, ...over,
});

const job = (over: Record<string, unknown> = {}, chain: Record<string, unknown> = {}) => ({
  job: {
    id: JOB_ID, zone_id: 'z1', kind: 'run', status: 'running',
    template: { id: 't1', name: 'deploy-site' }, run_group_id: 'g1', trigger: 'manual',
    actor_name: 'Ada', hosts: [], stats: {}, created_at: '2026-09-15T08:00:00Z', group: [],
    chain: { parent: null, follow_ups: [], ...chain },
    ...over,
  },
});

function routes(over: Record<string, MockRoute> = {}): Record<string, MockRoute> {
  return {
    'GET /api/v1/orgs/acme': org(),
    'GET /api/v1/orgs/acme/zones': { zones: [] },
    [`GET ${JOB_PATH}`]: job(),
    [`GET ${JOB_PATH}/events`]: { events: [] },
    ...over,
  };
}

const originalEventSource = globalThis.EventSource;
beforeEach(() => {
  globalThis.EventSource = FakeEventSource as unknown as typeof EventSource;
});
afterEach(() => {
  globalThis.EventSource = originalEventSource;
  FakeEventSource.reset();
});

describe('RunChainSection', () => {
  it('lists the follow-ups in order with their state and reason, and links a started one', async () => {
    mockApi(routes({
      [`GET ${JOB_PATH}`]: job({ status: 'succeeded' }, {
        follow_ups: [
          followUp({ id: 'a', template_name: 'first', state: 'started', child_first_job_id: CHILD_ID }),
          followUp({ id: 'b', template_name: 'second', state: 'due', reason: 'waiting for the active run of second' }),
          followUp({ id: 'c', template_name: 'third', state: 'skipped', reason: 'the run failed' }),
          followUp({ id: 'd', template_name: 'fourth', state: 'dismissed', reason: 'dismissed by Ada' }),
        ],
      }),
    }));
    renderRunPage(JOB_ID);

    const items = within(await screen.findByRole('region', { name: 'Follow-ups' })).getAllByRole('listitem');
    expect(items.map((li) => li.textContent)).toEqual([
      expect.stringMatching(/^Startedfirst2 hostsView run$/),
      expect.stringMatching(/^Duesecond2 hostswaiting for the active run of second$/),
      expect.stringMatching(/^Skippedthird2 hoststhe run failed$/),
      expect.stringMatching(/^Dismissedfourth2 hostsdismissed by Ada$/),
    ]);
    expect(within(items[0]).getByRole('link')).toHaveAttribute('href', `/o/acme/runs/${CHILD_ID}`);
    expect(screen.getByRole('link', { name: /^Follow-ups/ })).toBeInTheDocument();
  });

  it('keeps polling a finished run while a follow-up waits, and stops once it started', async () => {
    let started = false;
    mockApi(routes({
      [`GET ${JOB_PATH}`]: () =>
        job({ status: 'succeeded' }, {
          follow_ups: [started ? followUp({ state: 'started', child_first_job_id: CHILD_ID }) : followUp({ state: 'pending' })],
        }),
    }));
    renderRunPage(JOB_ID);
    const region = await screen.findByRole('region', { name: 'Follow-ups' });
    expect(within(region).getByText('Waiting')).toBeInTheDocument();
    started = true;
    expect(await within(region).findByText('Started', undefined, { timeout: 4000 })).toBeInTheDocument();
  }, 8000);

  it('shows no section and no nav entry without follow-ups', async () => {
    mockApi(routes());
    renderRunPage(JOB_ID);
    await screen.findByText('deploy-site');
    expect(screen.queryByRole('region', { name: 'Follow-ups' })).not.toBeInTheDocument();
  });
});

describe('RunSidebar chain', () => {
  it('links to the parent run and names a deleted parent template neutrally', async () => {
    mockApi(routes({
      [`GET ${JOB_PATH}`]: job({ trigger: 'chain', status: 'succeeded' }, {
        parent: { group_id: 'g0', first_job_id: PARENT_ID, template_id: 't0', template_name: '' },
      }),
    }));
    renderRunPage(JOB_ID);
    const link = await within(await screen.findByRole('complementary')).findByRole('link', { name: '(deleted template)' });
    expect(link).toHaveAttribute('href', `/o/acme/runs/${PARENT_ID}`);
  });

  it('names the parent template', async () => {
    mockApi(routes({
      [`GET ${JOB_PATH}`]: job({ trigger: 'chain' }, { parent: { group_id: 'g0', first_job_id: PARENT_ID, template_name: 'build' } }),
    }));
    renderRunPage(JOB_ID);
    const side = await screen.findByRole('complementary');
    expect(await within(side).findByRole('link', { name: 'build' })).toBeInTheDocument();
    expect(side).toHaveTextContent('Follow-up of build');
  });

  const pending = { follow_ups: [followUp({})] };

  it('confirms, posts the dismissal, toasts the count and refetches', async () => {
    const user = userEvent.setup();
    let dismissed = false;
    mockApi(routes({
      [`GET ${JOB_PATH}`]: () => job({}, dismissed ? { follow_ups: [followUp({ state: 'dismissed', reason: 'dismissed by Ada' })] } : pending),
      [`POST ${JOB_PATH}/follow-ups/dismiss`]: () => {
        dismissed = true;
        return { dismissed: 1 };
      },
    }));
    renderRunPage(JOB_ID);
    await user.click(await screen.findByRole('button', { name: 'Do not start follow-ups' }));
    await acceptConfirm(user);
    expect(await screen.findByText('1 follow-up dismissed')).toBeInTheDocument();
    expect(await screen.findByText('dismissed by Ada')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Do not start follow-ups' })).not.toBeInTheDocument();
  });

  it('posts nothing when the confirmation is declined', async () => {
    const user = userEvent.setup();
    mockApi(routes({ [`GET ${JOB_PATH}`]: job({}, pending), [`POST ${JOB_PATH}/follow-ups/dismiss`]: { dismissed: 1 } }));
    renderRunPage(JOB_ID);
    await user.click(await screen.findByRole('button', { name: 'Do not start follow-ups' }));
    await declineConfirm(user);
    expect(fetchMock.mock.calls.some(([r]) => (r as Request).method === 'POST')).toBe(false);
  });

  it('shows the server message when the run has finished meanwhile (409)', async () => {
    const user = userEvent.setup();
    mockApi(routes({
      [`GET ${JOB_PATH}`]: job({}, pending),
      [`POST ${JOB_PATH}/follow-ups/dismiss`]: () =>
        new Response(JSON.stringify({ title: 'Conflict', status: 409, detail: 'the run has finished' }), {
          status: 409,
          headers: { 'Content-Type': 'application/problem+json' },
        }),
    }));
    renderRunPage(JOB_ID);
    await user.click(await screen.findByRole('button', { name: 'Do not start follow-ups' }));
    await acceptConfirm(user);
    expect(await screen.findByText(/the run has finished/)).toBeInTheDocument();
  });

  it.each([
    ['the run is finished', job({ status: 'succeeded' }, pending), 'operator'],
    ['no follow-up is pending', job({}, { follow_ups: [followUp({ state: 'due' })] }), 'operator'],
    ['the role may not run jobs', job({}, pending), 'viewer'],
  ])('offers no dismissal when %s', async (_why, body, role) => {
    mockApi(routes({ 'GET /api/v1/orgs/acme': org(role), [`GET ${JOB_PATH}`]: body }));
    renderRunPage(JOB_ID);
    await screen.findByRole('region', { name: 'Follow-ups' });
    expect(screen.queryByRole('button', { name: 'Do not start follow-ups' })).not.toBeInTheDocument();
  });
});
