import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest, type MockRoute } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { findCodeEditor, replaceCode } from '../../test/codeEditor.ts';
import { FakeEventSource } from '../runs/runPageTestKit.tsx';
import { job, renderAt, routes, template } from './playbookPageTestKit.tsx';

const HOSTS = { hosts: [{ id: 'h1', name: 'web-01' }, { id: 'h2', name: 'web-02' }], next_cursor: '' };

const changed = (seq: number, host: string, task: string, diff: unknown) => ({
  seq, attempt: 1, at: '2026-11-22T10:00:00Z', host, task, state: 'changed',
  payload: { event: 'runner_on_ok', event_data: { host, task, res: { changed: true, diff } } },
});

const EVENTS = [
  { seq: 1, attempt: 1, at: '2026-11-22T10:00:00Z', host: 'web-01', task: 'Install package', state: 'ok', payload: { event: 'runner_on_ok' } },
  changed(2, 'web-01', 'Render nginx config', [{ before: 'listen 80;\nroot /srv;\n', after: 'listen 8080;\nroot /srv;\n', before_header: '/etc/nginx/site.conf', after_header: '/etc/nginx/site.conf' }]),
  changed(3, 'web-02', 'Render nginx config', { prepared: '--- old\n+++ new\n@@ -1 +1 @@\n-user www;\n+user nginx;\n' }),
  changed(4, 'web-02', 'Restart service', undefined),
];

const STATS = {
  host_counts: {
    h1: { ok: 4, changed: 1, skipped: 1, failed: 0, unreachable: 0 },
    h2: { ok: 2, changed: 2, skipped: 0, failed: 1, unreachable: 1 },
  },
};

const detail = (id: string, extra = {}) => ({
  job: {
    id, kind: 'run', status: 'succeeded', trigger: 'manual', check_mode: true, created_at: '2026-11-22T10:00:00Z', run_group_id: id, zone_id: 'z1',
    template: { id: 't1', name: 'Deploy web' }, hosts: ['h1', 'h2'], stats: STATS, attempts: 1, cancel_stops_play: true, chain: { follow_ups: [] }, group: [],
    vault_identities: [], vault_identities_source: 'recorded', ...extra,
  },
});

/** Routes for a page whose newest dry run is `d1` (or none). */
function dryRoutes(role = 'operator', opts: { latest?: string | null; detailOf?: Record<string, unknown>; extra?: Record<string, MockRoute> } = {}) {
  const asked: URLSearchParams[] = [];
  const latest = opts.latest === undefined ? 'd1' : opts.latest;
  return {
    asked,
    routes: routes(role, {
      'GET /api/v1/orgs/acme/jobs': (req: MockRequest) => {
        asked.push(req.search);
        if (req.search.get('check_mode') === 'true') return { jobs: latest ? [job(latest, { check_mode: true })] : [], next_cursor: '' };
        return { jobs: [], next_cursor: '', total: 0 };
      },
      'GET /api/v1/orgs/acme/jobs/d1': opts.detailOf ?? detail('d1'),
      'GET /api/v1/orgs/acme/jobs/d1/events': { events: EVENTS },
      'GET /api/v1/orgs/acme/hosts': HOSTS,
      ...opts.extra,
    }),
  };
}

const form = () => screen.findByRole('form', { name: 'Dry run' });

/** Waits for the templates, then presses Start dry run. */
async function startIn(f: HTMLElement, user: ReturnType<typeof userEvent.setup>) {
  const button = within(f).getByRole('button', { name: 'Start dry run' });
  await waitFor(() => expect(button).toBeEnabled());
  await user.click(button);
}

describe('PlaybookPage Dry run tab', () => {
  beforeEach(() => {
    FakeEventSource.reset();
    vi.stubGlobal('EventSource', FakeEventSource);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('opens from ?tab=dry-run, between Editor and Runs, and explains check mode', async () => {
    mockApi(dryRoutes().routes);
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    expect(await screen.findByRole('tab', { name: 'Dry run' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getAllByRole('tab').map((t) => t.textContent?.replace(/\d+$/, ''))).toEqual(['Editor', 'Dry run', 'Runs', 'Used by']);
    expect(within(await form()).getByText(/Ansible shows what it would change, but changes nothing/)).toBeInTheDocument();
  });

  it('offers only the templates that use the playbook, none chosen while there are several', async () => {
    const user = userEvent.setup();
    mockApi(dryRoutes('operator', { extra: { 'GET /api/v1/orgs/acme/job-templates': { templates: [template('t1', 'Deploy web', 'p1'), template('t2', 'Deploy staging', 'p1'), template('t9', 'Other', 'p9')] } } }).routes);
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const f = await form();
    const select = within(f).getByLabelText('Template (brings variables and credentials)');
    expect(select).toHaveValue('');
    await user.click(select);
    const listbox = document.getElementById(select.getAttribute('aria-controls') ?? '')!;
    expect(within(listbox).getAllByRole('option').map((o) => o.textContent)).toEqual(['Choose a template', 'Deploy web', 'Deploy staging']);
  });

  it('links to creating a template when none uses the playbook', async () => {
    mockApi(dryRoutes('operator', { latest: null }).routes);
    renderAt('/o/acme/playbooks/p2?tab=dry-run');
    const f = await form();
    expect(await within(f).findByRole('link', { name: 'Create a template' })).toHaveAttribute('href', '/o/acme/templates?create=p2');
    expect(within(f).getByRole('button', { name: 'Start dry run' })).toBeDisabled();
  });

  it('posts the exact body and shows the started run', async () => {
    const user = userEvent.setup();
    const sent: unknown[] = [];
    const { routes: r } = dryRoutes('operator', {
      extra: {
        'POST /api/v1/orgs/acme/playbooks/p1/dry-run': (req: MockRequest) => (sent.push(req.body), { jobs: [job('d2', { status: 'queued', check_mode: true })] }),
        'GET /api/v1/orgs/acme/jobs/d2': detail('d2', { status: 'queued', created_at: new Date().toISOString(), stats: {} }),
        'GET /api/v1/orgs/acme/jobs/d2/events': { events: [] },
      },
    });
    mockApi(r);
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const f = await form();
    await waitFor(() => expect(within(f).getByLabelText('Template (brings variables and credentials)')).toHaveValue('Deploy web'));
    const diff = within(f).getByRole('checkbox', { name: 'Show differences (diff)' });
    expect(diff).toBeChecked();
    const config = await within(f).findByRole('button', { name: 'config' });
    expect(config).toHaveAttribute('aria-pressed', 'false');
    await user.click(config);
    expect(config).toHaveAttribute('aria-pressed', 'true');
    await user.click(await within(f).findByRole('checkbox', { name: 'web-01' }));
    await startIn(f, user);
    await waitFor(() => expect(sent).toEqual([{ template_id: 't1', host_ids: ['h1'], tags: ['config'], diff: true }]));
    const result = await screen.findByRole('region', { name: 'Latest dry run' });
    await waitFor(() => expect(within(result).getByRole('link', { name: 'Open run' })).toHaveAttribute('href', '/o/acme/runs/d2'));
  });

  it('sends neither hosts, tags nor a draft when none are chosen, and diff off when unticked', async () => {
    const user = userEvent.setup();
    const sent: unknown[] = [];
    mockApi(dryRoutes('operator', { extra: { 'POST /api/v1/orgs/acme/playbooks/p1/dry-run': (req: MockRequest) => (sent.push(req.body), { jobs: [job('d1')] }) } }).routes);
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const f = await form();
    expect(within(f).queryByRole('checkbox', { name: 'Check the unsaved draft' })).not.toBeInTheDocument();
    await user.click(within(f).getByRole('checkbox', { name: 'Show differences (diff)' }));
    await startIn(f, user);
    await waitFor(() => expect(sent).toEqual([{ template_id: 't1', diff: false }]));
  });

  it('lists the tasks that run for real in a dry run', async () => {
    mockApi(dryRoutes().routes);
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const f = await form();
    const warning = await within(f).findByRole('list', { name: 'These tasks run for real, even in a dry run (check_mode: false):' });
    expect(within(warning).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['Check config · line 13']);
  });

  it('offers the unsaved draft while the editor holds one, and sends it when ticked', async () => {
    const user = userEvent.setup();
    const sent: unknown[] = [];
    mockApi(dryRoutes('operator', { extra: { 'POST /api/v1/orgs/acme/playbooks/p1/dry-run': (req: MockRequest) => (sent.push(req.body), { jobs: [job('d1')] }) } }).routes);
    renderAt('/o/acme/playbooks/p1');
    const editor = await findCodeEditor('Playbook YAML');
    const changedText = '- name: Install nginx\n  hosts: db\n  tasks: []\n';
    replaceCode(editor, changedText);
    await user.click(screen.getByRole('tab', { name: 'Dry run' }));
    const f = await form();
    const draft = within(f).getByRole('checkbox', { name: 'Check the unsaved draft' });
    expect(draft).not.toBeChecked();
    await user.click(draft);
    await startIn(f, user);
    await waitFor(() => expect(sent).toEqual([{ template_id: 't1', diff: true, draft: changedText }]));
  });

  it('tells a reader that starting one needs more, and still shows the latest', async () => {
    mockApi(dryRoutes('viewer').routes);
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    expect(await screen.findByText(/Starting a dry run needs the operator role/)).toBeInTheDocument();
    expect(screen.queryByRole('form', { name: 'Dry run' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Start dry run' })).not.toBeInTheDocument();
    expect(await screen.findByRole('region', { name: 'Latest dry run' })).toBeInTheDocument();
  });

  it('shows the newest dry run of this playbook: counts over the hosts, each change with its diff', async () => {
    const { asked, routes: r } = dryRoutes();
    mockApi(r);
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const result = await screen.findByRole('region', { name: 'Latest dry run' });
    await waitFor(() => expect(asked.some((s) => s.get('check_mode') === 'true' && s.get('playbook_id') === 'p1' && s.get('limit') === '1')).toBe(true));
    const counts = await within(result).findByRole('list', { name: 'Counts over all hosts' });
    expect(within(counts).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['6ok', '3would change', '1skipped', '1failed', '1unreachable']);
    const changes = await within(result).findByRole('list', { name: 'What would change' });
    const items = within(changes).getAllByRole('listitem').filter((li) => li.parentElement === changes);
    expect(items.map((li) => li.querySelector('h4')?.textContent)).toEqual([
      'Render nginx config · web-01 · would change',
      'Render nginx config · web-02 · would change',
      'Restart service · web-02 · would change',
    ]);
    expect(within(items[0]).getByText('-listen 80;')).toBeInTheDocument();
    expect(within(items[0]).getByText('+listen 8080;')).toBeInTheDocument();
    expect(within(items[1]).getByText('+user nginx;')).toBeInTheDocument();
    expect(within(items[2]).getByText('No differences reported for this task.')).toBeInTheDocument();
    expect(within(result).getByRole('link', { name: 'Open run' })).toHaveAttribute('href', '/o/acme/runs/d1');
  });

  it('says when there is no dry run yet', async () => {
    mockApi(dryRoutes('operator', { latest: null }).routes);
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const result = await screen.findByRole('region', { name: 'Latest dry run' });
    expect(await within(result).findByText('No dry run yet.')).toBeInTheDocument();
  });

  it('follows a running dry run live', async () => {
    mockApi(dryRoutes('operator', { detailOf: detail('d1', { status: 'running', stats: {} }), extra: { 'GET /api/v1/orgs/acme/jobs/d1/events': { events: [] } } }).routes);
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const result = await screen.findByRole('region', { name: 'Latest dry run' });
    expect(await within(result).findByText('Running: the result follows live.')).toBeInTheDocument();
    await waitFor(() => expect(FakeEventSource.last()?.url).toBe('/api/v1/orgs/acme/jobs/d1/stream'));
    FakeEventSource.last()!.emit('log', changed(1, 'web-01', 'Render nginx config', { before: 'a\n', after: 'b\n' }));
    expect(await within(result).findByText('+b')).toBeInTheDocument();
    const counts = within(result).getByRole('list', { name: 'Counts over all hosts' });
    expect(within(counts).getAllByRole('listitem')[1]).toHaveTextContent('1would change');
  });

  it('says it waits for a worker that supports dry runs while one stays queued', async () => {
    mockApi(dryRoutes('operator', { detailOf: detail('d1', { status: 'queued', stats: {}, created_at: '2026-01-01T00:00:00Z' }), extra: { 'GET /api/v1/orgs/acme/jobs/d1/events': { events: [] } } }).routes);
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    expect(await screen.findByText(/Waiting for a worker that supports dry runs/)).toBeInTheDocument();
  });

  it('reports a refused start', async () => {
    const user = userEvent.setup();
    const refuse = () => new Response(JSON.stringify({ status: 422, detail: 'a host is not among the template targets' }), { status: 422, headers: { 'Content-Type': 'application/json' } });
    mockApi(dryRoutes('operator', { extra: { 'POST /api/v1/orgs/acme/playbooks/p1/dry-run': refuse } }).routes);
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const f = await form();
    await startIn(f, user);
    expect(await within(f).findByRole('alert')).toHaveTextContent('a host is not among the template targets');
  });

  it('speaks German', async () => {
    mockApi(dryRoutes().routes);
    renderAt('/o/acme/playbooks/p1?tab=dry-run', 'de');
    const f = await screen.findByRole('form', { name: 'Probelauf' });
    expect(within(f).getByRole('button', { name: 'Probelauf starten' })).toBeInTheDocument();
    expect(within(f).getByRole('checkbox', { name: 'Unterschiede zeigen (diff)' })).toBeChecked();
    const result = await screen.findByRole('region', { name: 'Letzter Probelauf' });
    expect(await within(result).findByText('Render nginx config · web-01 · würde ändern')).toBeInTheDocument();
    expect(within(result).getByRole('link', { name: 'Zum Lauf' })).toBeInTheDocument();
  });

  it('picks a template with the Select', async () => {
    const user = userEvent.setup();
    const sent: unknown[] = [];
    mockApi(
      dryRoutes('operator', {
        extra: {
          'GET /api/v1/orgs/acme/job-templates': { templates: [template('t1', 'Deploy web', 'p1'), template('t2', 'Deploy staging', 'p1')] },
          'POST /api/v1/orgs/acme/playbooks/p1/dry-run': (req: MockRequest) => (sent.push(req.body), { jobs: [job('d1')] }),
        },
      }).routes,
    );
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const f = await form();
    expect(within(f).getByRole('button', { name: 'Start dry run' })).toBeDisabled();
    await pickOption(within(f), 'Template (brings variables and credentials)', 'Deploy staging');
    await startIn(f, user);
    await waitFor(() => expect(sent).toEqual([{ template_id: 't2', diff: true }]));
  });
});
