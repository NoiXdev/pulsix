import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { findCodeEditor, replaceCode } from '../../test/codeEditor.ts';
import { editorPlaybook, renderAt, routes, template } from './playbookPageTestKit.tsx';

const problem = (status: number, detail: string) =>
  new Response(JSON.stringify({ status, detail }), { status, headers: { 'Content-Type': 'application/json' } });

const runRoute = (started: string[], id: string) => ({
  [`POST /api/v1/orgs/acme/job-templates/${id}/run`]: () => {
    started.push(id);
    return { jobs: [{ id: 'j9' }] };
  },
});

describe('PlaybookPage Run…', () => {
  it('opens the picker with the one template chosen, and runs nothing until asked', async () => {
    const user = userEvent.setup();
    const started: string[] = [];
    mockApi(routes('operator', runRoute(started, 't1')));
    renderAt('/o/acme/playbooks/p1');
    await user.click(await screen.findByRole('button', { name: 'Run…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Run Install nginx' });
    expect(within(dialog).getByLabelText('Template')).toHaveValue('Deploy web');
    expect(within(dialog).queryByText(/unsaved changes/)).not.toBeInTheDocument();
    expect(started).toEqual([]);
    await user.click(within(dialog).getByRole('button', { name: 'Run' }));
    await waitFor(() => expect(started).toEqual(['t1']));
    expect(await screen.findByText('RUN')).toBeInTheDocument();
  });

  it('lets the person pick when several templates use the playbook', async () => {
    const user = userEvent.setup();
    const started: string[] = [];
    mockApi(
      routes('operator', {
        'GET /api/v1/orgs/acme/job-templates': { templates: [template('t1', 'Deploy web', 'p1'), template('t2', 'Deploy staging', 'p1')] },
        ...runRoute(started, 't2'),
      }),
    );
    renderAt('/o/acme/playbooks/p1');
    await user.click(await screen.findByRole('button', { name: 'Run…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Run Install nginx' });
    expect(within(dialog).getByRole('button', { name: 'Run' })).toBeDisabled();
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

  it('says that the saved version runs while the draft has unsaved changes', async () => {
    const user = userEvent.setup();
    mockApi(routes());
    renderAt('/o/acme/playbooks/p1');
    replaceCode(await findCodeEditor('Playbook YAML'), '- hosts: db\n');
    await screen.findByText('Unsaved');
    await user.click(screen.getByRole('button', { name: 'Run…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Run Install nginx' });
    expect(within(dialog).getByText('The saved version runs: your unsaved changes are not included.')).toBeInTheDocument();
  });

  it('does not save with Mod-S while the Run dialog is open', async () => {
    const user = userEvent.setup();
    const sent: unknown[] = [];
    mockApi(routes('operator', { 'PUT /api/v1/orgs/acme/playbooks/p1': (req: MockRequest) => (sent.push(req.body), { playbook: editorPlaybook() }) }));
    renderAt('/o/acme/playbooks/p1');
    replaceCode(await findCodeEditor('Playbook YAML'), '- hosts: db\n');
    await screen.findByText('Unsaved');
    await user.click(screen.getByRole('button', { name: 'Run…' }));
    await screen.findByRole('dialog', { name: 'Run Install nginx' });
    await user.keyboard('{Control>}s{/Control}');
    await new Promise((r) => setTimeout(r, 50));
    expect(sent).toEqual([]);
  });

  it('speaks German in the dialog', async () => {
    const user = userEvent.setup();
    mockApi(routes());
    renderAt('/o/acme/playbooks/p1', 'de');
    replaceCode(await findCodeEditor('Playbook-YAML'), '- hosts: db\n');
    await user.click(await screen.findByRole('button', { name: 'Ausführen…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Install nginx ausführen' });
    expect(within(dialog).getByText('Es läuft die gespeicherte Fassung: deine ungespeicherten Änderungen sind nicht dabei.')).toBeInTheDocument();
  });
});

describe('PlaybookPage when the templates cannot be read', () => {
  const failing = () => routes('operator', { 'GET /api/v1/orgs/acme/job-templates': () => problem(500, 'database unavailable') });

  it('says so on the Used by tab instead of loading forever', async () => {
    mockApi(failing());
    renderAt('/o/acme/playbooks/p1?tab=used');
    expect(await screen.findByRole('alert')).toHaveTextContent('database unavailable');
    expect(screen.queryByRole('table', { name: 'Templates using this playbook' })).not.toBeInTheDocument();
  });

  it('keeps Run… and Delete disabled and says why', async () => {
    const user = userEvent.setup();
    mockApi(failing());
    renderAt('/o/acme/playbooks/p2');
    await screen.findByText('Templates could not be read.');
    const run = screen.getByRole('button', { name: 'Run…' });
    expect(run).toBeDisabled();
    expect(run).toHaveAccessibleDescription('Templates could not be read.');
    await user.click(screen.getByRole('button', { name: 'More actions for playbooks/site.yml' }));
    const item = screen.getByRole('menuitem', { name: /Delete…/ });
    expect(item).toHaveAttribute('aria-disabled', 'true');
    expect(item).toHaveTextContent('Templates could not be read.');
  });

  it("says when a template's schedules or triggers cannot be read", async () => {
    mockApi(
      routes('operator', {
        'GET /api/v1/orgs/acme/schedules': () => problem(500, 'boom'),
        'GET /api/v1/orgs/acme/triggers': () => problem(500, 'boom'),
      }),
    );
    renderAt('/o/acme/playbooks/p1?tab=used');
    const list = await screen.findByRole('table', { name: 'Templates using this playbook' });
    await waitFor(() => expect(within(list).getAllByText('could not be read')).toHaveLength(2));
  });
});
