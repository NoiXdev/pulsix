import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { acceptConfirm } from '../../test/confirmDialog.ts';
import { pickOption } from '../../test/pickOption.ts';
import { job, renderAt, repoPlaybook, routes, template } from './playbookPageTestKit.tsx';

const problem = (status: number, detail: string) =>
  new Response(JSON.stringify({ status, detail }), { status, headers: { 'Content-Type': 'application/json' } });

describe('PlaybookPage tabs and actions', () => {
  it('lists the runs of this playbook from the server filter, paged by the server', async () => {
    const asked: URLSearchParams[] = [];
    mockApi(
      routes('operator', {
        'GET /api/v1/orgs/acme/jobs': (req: MockRequest) => {
          asked.push(req.search);
          return { jobs: [job('aaaaaaaa-0000-0000-0000-000000000001'), job('bbbbbbbb-0000-0000-0000-000000000002', { status: 'failed' })], next_cursor: '', total: 2 };
        },
      }),
    );
    renderAt('/o/acme/playbooks/p1?tab=runs');
    const list = await screen.findByRole('table', { name: 'Runs of this playbook' });
    expect(await within(list).findByRole('link', { name: 'aaaaaaaa' })).toHaveAttribute('href', '/o/acme/runs/aaaaaaaa-0000-0000-0000-000000000001');
    const paged = asked.filter((s) => s.has('page'));
    expect(paged.length).toBeGreaterThan(0);
    expect(paged.every((s) => s.get('playbook_id') === 'p1')).toBe(true);
  });

  it('narrows the runs by status and keeps the playbook filter', async () => {
    const asked: URLSearchParams[] = [];
    mockApi(
      routes('operator', {
        'GET /api/v1/orgs/acme/jobs': (req: MockRequest) => {
          asked.push(req.search);
          return { jobs: [], next_cursor: '', total: 0 };
        },
      }),
    );
    renderAt('/o/acme/playbooks/p1?tab=runs');
    await screen.findByText('No template has run this playbook yet.');
    await pickOption(screen, 'Filter by status', { value: 'failed' });
    await waitFor(() => expect(asked.some((s) => s.has('page') && s.get('status') === 'failed' && s.get('playbook_id') === 'p1')).toBe(true));
  });

  it('lists the templates using the playbook with their schedules and webhook triggers', async () => {
    const asked: string[] = [];
    mockApi(
      routes('operator', {
        'GET /api/v1/orgs/acme/schedules': (req: MockRequest) => {
          asked.push(`schedules ${req.search.get('template_id')}`);
          return { schedules: [{ id: 's1', name: 'Nightly deploy', template_id: 't1' }] };
        },
        'GET /api/v1/orgs/acme/triggers': (req: MockRequest) => {
          asked.push(`triggers ${req.search.get('template_id')}`);
          return { triggers: [{ id: 'w1', name: 'Git push', kind: 'template_run', template_id: 't1' }] };
        },
      }),
    );
    renderAt('/o/acme/playbooks/p1?tab=used');
    const list = await screen.findByRole('table', { name: 'Templates using this playbook' });
    expect(await within(list).findByText('Deploy web')).toBeInTheDocument();
    expect(within(list).queryByText('Other')).not.toBeInTheDocument();
    expect(await within(list).findByText('Nightly deploy')).toBeInTheDocument();
    expect(await within(list).findByText('Git push')).toBeInTheDocument();
    expect(within(list).getByText('web')).toBeInTheDocument();
    // Only the listed template's schedules and triggers are read.
    expect(asked.sort()).toEqual(['schedules t1', 'triggers t1']);
    expect(screen.getByRole('link', { name: 'Create template…' })).toHaveAttribute('href', '/o/acme/templates?create=p1');
    expect(screen.getByRole('tab', { name: /Used by/ })).toHaveTextContent('1');
  });

  it('says when no template uses the playbook', async () => {
    mockApi(routes());
    renderAt('/o/acme/playbooks/p2?tab=used');
    expect(await screen.findByText('No template uses this playbook yet.')).toBeInTheDocument();
  });

  it('opens a template from the used-by list in its dialog', async () => {
    const user = userEvent.setup();
    mockApi(routes('operator', { 'GET /api/v1/orgs/acme/job-templates/t1': { template: template('t1', 'Deploy web', 'p1') }, 'GET /api/v1/orgs/acme/credentials': { credentials: [] } }));
    renderAt('/o/acme/playbooks/p1?tab=used');
    await user.click(await screen.findByRole('button', { name: 'Deploy web' }));
    expect(await screen.findByRole('dialog', { name: 'Deploy web' })).toBeInTheDocument();
  });

  it("opens a template's variables page for a reader", async () => {
    mockApi(routes('viewer'));
    renderAt('/o/acme/playbooks/p1?tab=used');
    expect(await screen.findByRole('link', { name: 'Deploy web' })).toHaveAttribute('href', '/o/acme/templates/t1/variables');
  });

  it('runs the one template that uses the playbook at once', async () => {
    const user = userEvent.setup();
    const started: string[] = [];
    mockApi(
      routes('operator', {
        'POST /api/v1/orgs/acme/job-templates/t1/run': () => {
          started.push('t1');
          return { jobs: [{ id: 'j9' }] };
        },
      }),
    );
    renderAt('/o/acme/playbooks/p1');
    await user.click(await screen.findByRole('button', { name: 'Run…' }));
    await waitFor(() => expect(started).toEqual(['t1']));
    expect(await screen.findByText('RUN')).toBeInTheDocument();
  });

  it('lets the person pick when several templates use the playbook', async () => {
    const user = userEvent.setup();
    const started: string[] = [];
    mockApi(
      routes('operator', {
        'GET /api/v1/orgs/acme/job-templates': { templates: [template('t1', 'Deploy web', 'p1'), template('t2', 'Deploy staging', 'p1')] },
        'POST /api/v1/orgs/acme/job-templates/t2/run': () => {
          started.push('t2');
          return { jobs: [{ id: 'j9' }] };
        },
      }),
    );
    renderAt('/o/acme/playbooks/p1');
    await user.click(await screen.findByRole('button', { name: 'Run…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Run Install nginx' });
    await pickOption(within(dialog), 'Template', 'Deploy staging');
    await user.click(within(dialog).getByRole('button', { name: 'Run' }));
    await waitFor(() => expect(started).toEqual(['t2']));
  });

  it('links to creating a template when none uses the playbook', async () => {
    const user = userEvent.setup();
    mockApi(routes());
    renderAt('/o/acme/playbooks/p2');
    await user.click(await screen.findByRole('button', { name: 'Run…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Run playbooks/site.yml' });
    expect(within(dialog).getByRole('link', { name: 'Create a template' })).toHaveAttribute('href', '/o/acme/templates?create=p2');
  });

  it('refuses Delete while a template uses the playbook, and says why', async () => {
    const user = userEvent.setup();
    const deleted: string[] = [];
    mockApi(routes('operator', { 'DELETE /api/v1/orgs/acme/playbooks/p1': () => (deleted.push('p1'), { ok: true }) }));
    renderAt('/o/acme/playbooks/p1');
    await user.click(await screen.findByRole('button', { name: 'More actions for Install nginx' }));
    const item = screen.getByRole('menuitem', { name: /Delete…/ });
    expect(item).toHaveAttribute('aria-disabled', 'true');
    expect(item).toHaveTextContent('A template uses it: remove it from that template first.');
    await user.click(item);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(deleted).toEqual([]);
  });

  it('keeps Delete disabled until the templates are known', async () => {
    const user = userEvent.setup();
    mockApi(routes('operator', { 'GET /api/v1/orgs/acme/job-templates': () => new Promise(() => {}) }));
    renderAt('/o/acme/playbooks/p2');
    await user.click(await screen.findByRole('button', { name: 'More actions for playbooks/site.yml' }));
    expect(screen.getByRole('menuitem', { name: 'Delete…' })).toHaveAttribute('aria-disabled', 'true');
  });

  it('does not read the deleted playbook again on its way out', async () => {
    const user = userEvent.setup();
    let reads = 0;
    mockApi(
      routes('operator', {
        'GET /api/v1/orgs/acme/playbooks/p2': () => (reads++, { playbook: repoPlaybook() }),
        'DELETE /api/v1/orgs/acme/playbooks/p2': () => ({ ok: true }),
      }),
    );
    const { qc } = renderAt('/o/acme/playbooks/p2');
    await user.click(await screen.findByRole('button', { name: 'More actions for playbooks/site.yml' }));
    await user.click(screen.getByRole('menuitem', { name: 'Delete…' }));
    const before = reads;
    await acceptConfirm(user);
    expect(await screen.findByText('PLAYBOOKS')).toBeInTheDocument();
    expect(screen.queryByText('Not found')).not.toBeInTheDocument();
    expect(reads).toBe(before);
    expect(qc.getQueryData(['playbooks', 'acme', 'p2'])).toBeUndefined();
  });

  it('deletes an unused playbook after confirmation and goes back to the list', async () => {
    const user = userEvent.setup();
    const deleted: string[] = [];
    mockApi(routes('operator', { 'DELETE /api/v1/orgs/acme/playbooks/p2': () => (deleted.push('p2'), { ok: true }) }));
    renderAt('/o/acme/playbooks/p2');
    await user.click(await screen.findByRole('button', { name: 'More actions for playbooks/site.yml' }));
    await user.click(screen.getByRole('menuitem', { name: 'Delete…' }));
    await acceptConfirm(user);
    expect(await screen.findByText('PLAYBOOKS')).toBeInTheDocument();
    expect(deleted).toEqual(['p2']);
  });

  it("reports the server's refusal of a delete", async () => {
    const user = userEvent.setup();
    mockApi(routes('operator', { 'DELETE /api/v1/orgs/acme/playbooks/p2': () => problem(409, 'the playbook is still referenced by a job template') }));
    renderAt('/o/acme/playbooks/p2');
    await user.click(await screen.findByRole('button', { name: 'More actions for playbooks/site.yml' }));
    await user.click(screen.getByRole('menuitem', { name: 'Delete…' }));
    await acceptConfirm(user);
    expect(await screen.findByText('the playbook is still referenced by a job template')).toBeInTheDocument();
  });
});
