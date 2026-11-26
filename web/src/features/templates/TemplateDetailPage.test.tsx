import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi } from '../../test/mockApi.ts';
import { pageRoutes, renderTemplatePage, schedule, template, trigger } from './templatePageTestKit.tsx';

/** The tab strip, as label → count ('' for a tab without one). */
function tabItems(): Array<[string, string]> {
  const list = screen.getByRole('tablist', { name: 'Template sections' });
  return within(list)
    .getAllByRole('tab')
    .map((tab) => {
      const spans = tab.querySelectorAll('span');
      return [spans[0]?.textContent ?? '', spans[1]?.textContent ?? ''];
    });
}

const tab = (name: string) => screen.getByRole('tab', { name: new RegExp(`^${name}`) });
const panel = () => within(screen.getByRole('tabpanel'));
const findPanel = async () => within(await screen.findByRole('tabpanel'));

describe('TemplateDetailPage', () => {
  it('shows the header and the tabs, on Overview by default', async () => {
    const tpl = template({
      agent_enrollment: true,
      shared_template_id: 'shared-1',
      limit: 'web*',
      follow_ups: [{ template_id: 't2', template_name: 'notify', position: 0 }],
    });
    mockApi(pageRoutes({ tpl, over: { 'GET /api/v1/orgs/acme/schedules': { schedules: [schedule('s1'), schedule('s2')] } } }));
    const { router } = renderTemplatePage();

    expect(await screen.findByRole('heading', { level: 1, name: 'deploy-site' })).toBeInTheDocument();
    const badges = within(screen.getByTestId('template-badges'));
    expect(badges.getByText('Installation-managed')).toBeInTheDocument();
    expect(badges.getByText('Enrols agents')).toBeInTheDocument();
    expect(badges.getByText('1 follow-up')).toBeInTheDocument();

    const facts = within(screen.getByTestId('template-facts'));
    expect(await facts.findByRole('link', { name: 'site.yml' })).toHaveAttribute('href', '/o/acme/playbooks/pb1');
    expect(await facts.findByText('infra · main')).toBeInTheDocument();
    expect(facts.getByText('0123456789ab')).toBeInTheDocument();
    expect(facts.getByText(/^synced /)).toBeInTheDocument();
    expect(facts.getByText('whole organization · limit web*')).toBeInTheDocument();

    await waitFor(() =>
      expect(tabItems()).toEqual([
        ['Overview', ''],
        ['Variables', ''],
        ['Schedules', '2'],
        ['Triggers', '0'],
        ['Runs', ''],
        ['Settings', ''],
      ]),
    );
    expect(tab('Overview')).toHaveAttribute('aria-selected', 'true');
    expect(router.state.location.search).toEqual({});
    expect(screen.queryByRole('note', { name: /paused for review/ })).not.toBeInTheDocument();
  });

  it('names the hosts a group scope resolves to', async () => {
    const member = (host: string, name: string) => ({ group_id: 'g1', group_name: 'web', host_id: host, host_name: name, complete: true, orphaned: [], problems: [], revision: 1, vars: {} });
    mockApi(
      pageRoutes({
        tpl: template({ scope: { group_id: 'g1', host_ids: [] } }),
        over: {
          'GET /api/v1/orgs/acme/inventory/groups': { groups: [{ id: 'g1', name: 'web' }] },
          'GET /api/v1/orgs/acme/inventory/groups/g1/members': { members: ['d', 'a', 'c', 'b'].map((n) => member(`h-${n}`, `web-${n}`)) },
        },
      }),
    );
    renderTemplatePage();
    const facts = within(await screen.findByTestId('template-facts'));
    expect(await facts.findByText('web · web-a, web-b, web-c +1 more')).toBeInTheDocument();
  });

  it('says when the playbook is not synced yet and an editor playbook has no repository', async () => {
    mockApi(pageRoutes({ tpl: template({ playbook_id: '' }) }));
    renderTemplatePage();
    const facts = within(await screen.findByTestId('template-facts'));
    expect(facts.getByText('not synced yet')).toBeInTheDocument();
    expect(facts.queryByText('Source')).not.toBeInTheDocument();
  });

  it('names the playbook editor as the source of an editor playbook', async () => {
    mockApi(pageRoutes({ over: { 'GET /api/v1/orgs/acme/playbooks': { playbooks: [{ id: 'pb1', source: 'editor', summary: 'Install nginx', revision: 1, created_at: '', updated_at: '' }] } } }));
    renderTemplatePage();
    const facts = within(await screen.findByTestId('template-facts'));
    expect(await facts.findByRole('link', { name: 'Install nginx' })).toBeInTheDocument();
    expect(facts.getByText('Playbook editor')).toBeInTheDocument();
    expect(facts.queryByText('Commit')).not.toBeInTheDocument();
  });

  it('switches tabs and keeps the open one in the URL', async () => {
    const user = userEvent.setup();
    mockApi(pageRoutes());
    const { router } = renderTemplatePage();

    await user.click(await screen.findByRole('tab', { name: /^Settings/ }));
    expect(router.state.location.search).toEqual({ tab: 'settings' });
    expect(tab('Settings')).toHaveAttribute('aria-selected', 'true');
    expect(panel().getByRole('form', { name: 'Template settings' })).toBeInTheDocument();

    await user.click(tab('Overview'));
    expect(router.state.location.search).toEqual({});
  });

  it('opens the tab the URL names and falls back to Overview for an unknown one', async () => {
    mockApi(pageRoutes());
    renderTemplatePage('/o/acme/templates/t1?tab=runs');
    expect(await screen.findByRole('tab', { name: /^Runs/ })).toHaveAttribute('aria-selected', 'true');
  });

  it.each([
    ['variables', 'Open the variables page', '/o/acme/templates/t1/variables'],
    ['schedules', 'Open the schedules of this template', '/o/acme/schedules?template=t1'],
    ['runs', 'Open the runs of this template', '/o/acme/runs?template=t1'],
  ])('leads from the %s tab to where it lives for now', async (name, link, href) => {
    mockApi(pageRoutes());
    renderTemplatePage(`/o/acme/templates/t1?tab=${name}`);
    expect(await (await findPanel()).findByRole('link', { name: link })).toHaveAttribute('href', href);
  });

  it('opens the triggers dialog from the Triggers tab', async () => {
    const user = userEvent.setup();
    mockApi(pageRoutes({ over: { 'GET /api/v1/orgs/acme/triggers': { triggers: [trigger('w1')] } } }));
    renderTemplatePage('/o/acme/templates/t1?tab=triggers');
    await user.click(await (await findPanel()).findByRole('button', { name: 'Manage the webhook triggers' }));
    expect(await screen.findByRole('dialog', { name: 'Triggers · deploy-site' })).toBeInTheDocument();
  });

  it('says when an assistant change paused a schedule or trigger and links the review', async () => {
    mockApi(
      pageRoutes({
        over: {
          'GET /api/v1/orgs/acme/schedules': { schedules: [schedule('s1', { paused: true, paused_reason: 'assistant_change', review_request_id: 'rv1' }), schedule('s2')] },
          'GET /api/v1/orgs/acme/triggers': { triggers: [trigger('w1', { enabled: false, disabled_reason: 'assistant_change', review_request_id: 'rv1' })] },
        },
      }),
    );
    renderTemplatePage();
    const note = await screen.findByRole('note', { name: '2 schedules and triggers are paused for review' });
    expect(within(note).getByRole('link', { name: 'Open the review' })).toHaveAttribute('href', '/o/acme/runs/requests/rv1');
    await waitFor(() => expect(tab('Schedules').querySelector('[data-tone]')).toHaveAttribute('data-tone', 'warn'));
    expect(tab('Triggers').querySelector('[data-tone]')).toHaveAttribute('data-tone', 'warn');
  });

  it("links a template's pending requests when several reviews paused it", async () => {
    mockApi(
      pageRoutes({
        over: {
          'GET /api/v1/orgs/acme/schedules': {
            schedules: [schedule('s1', { paused: true, paused_reason: 'assistant_change', review_request_id: 'rv1' }), schedule('s2', { paused: true, paused_reason: 'assistant_change', review_request_id: 'rv2' })],
          },
        },
      }),
    );
    renderTemplatePage();
    const note = await screen.findByRole('note', { name: '2 schedules and triggers are paused for review' });
    expect(within(note).getByRole('link', { name: 'Open the reviews' })).toHaveAttribute('href', '/o/acme/runs/requests?template=t1&status=pending');
  });

  it('asks the schedules and triggers of this template only', async () => {
    const asked: string[] = [];
    mockApi(
      pageRoutes({
        over: {
          'GET /api/v1/orgs/acme/schedules': (req: { search: URLSearchParams }) => (asked.push(`schedules:${req.search.get('template_id')}`), { schedules: [] }),
          'GET /api/v1/orgs/acme/triggers': (req: { search: URLSearchParams }) => (asked.push(`triggers:${req.search.get('template_id')}`), { triggers: [] }),
          'GET /api/v1/orgs/acme/jobs': (req: { search: URLSearchParams }) => (asked.push(`jobs:${req.search.get('template_id')}`), { jobs: [], next_cursor: '' }),
        },
      }),
    );
    renderTemplatePage();
    await waitFor(() => expect([...new Set(asked)].sort()).toEqual(['jobs:t1', 'schedules:t1', 'triggers:t1']));
  });

  it('shows the error of a template that cannot be read', async () => {
    mockApi(pageRoutes({ over: { 'GET /api/v1/orgs/acme/job-templates/t1': () => new Response(JSON.stringify({ detail: 'template not found' }), { status: 404, headers: { 'Content-Type': 'application/problem+json' } }) } }));
    renderTemplatePage();
    expect(await screen.findByText(/template not found|not found/i)).toBeInTheDocument();
    expect(screen.queryByRole('tablist')).not.toBeInTheDocument();
  });
});
