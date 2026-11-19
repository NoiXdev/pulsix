import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { acceptConfirm, declineConfirm } from '../../test/confirmDialog.ts';
import { codeOf, findCodeEditor, replaceCode } from '../../test/codeEditor.ts';
import { INLINE, editorPlaybook, renderAt, routes } from './playbookPageTestKit.tsx';

const problem = (status: number, detail: string) =>
  new Response(JSON.stringify({ status, detail }), { status, headers: { 'Content-Type': 'application/json' } });

const heading = () => screen.findByRole('heading', { level: 1, name: 'Install nginx' });

describe('PlaybookPage', () => {
  it('shows the name, the pills, the summary line and the editor with the stored YAML', async () => {
    mockApi(routes());
    renderAt('/o/acme/playbooks/p1');
    await heading();
    expect(screen.getByText('Valid')).toBeInTheDocument();
    expect(within(screen.getByTestId('playbook-header')).getByText('Editor')).toBeInTheDocument();
    expect(screen.queryByText('Unsaved')).not.toBeInTheDocument();
    expect(screen.getByText(/^updated /)).toBeInTheDocument();
    expect(codeOf(await findCodeEditor('Playbook YAML'))).toBe(INLINE);
    expect(screen.getByRole('tab', { name: 'Editor', selected: true })).toBeInTheDocument();
  });

  it('keeps Save disabled while clean or invalid, and marks the draft unsaved', async () => {
    mockApi(routes());
    renderAt('/o/acme/playbooks/p1');
    const editor = await findCodeEditor('Playbook YAML');
    const save = screen.getByRole('button', { name: 'Save' });
    expect(save).toBeDisabled();
    replaceCode(editor, 'hosts: all');
    expect(await screen.findByText('Unsaved')).toBeInTheDocument();
    const check = screen.getByRole('region', { name: 'Check' });
    expect(within(check).getByText('expected a list of plays')).toBeInTheDocument();
    expect(save).toBeDisabled();
    replaceCode(editor, '- hosts: all\n');
    await waitFor(() => expect(save).toBeEnabled());
    expect(within(check).getByText('Parses as a list of plays — ready to save.')).toBeInTheDocument();
  });

  it('saves with Save, then the page is clean again', async () => {
    const user = userEvent.setup();
    const sent: unknown[] = [];
    mockApi(
      routes('operator', {
        'PUT /api/v1/orgs/acme/playbooks/p1': (req: MockRequest) => {
          sent.push(req.body);
          return { playbook: editorPlaybook({ inline: (req.body as { inline: string }).inline }) };
        },
      }),
    );
    renderAt('/o/acme/playbooks/p1');
    replaceCode(await findCodeEditor('Playbook YAML'), '- hosts: db\n');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(sent).toEqual([{ inline: '- hosts: db\n' }]));
    expect(await screen.findByText('Playbook saved')).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByText('Unsaved')).not.toBeInTheDocument());
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();
  });

  it('saves with Mod-S in the editor, and not while the preview refuses', async () => {
    const user = userEvent.setup();
    const sent: unknown[] = [];
    mockApi(
      routes('operator', {
        'PUT /api/v1/orgs/acme/playbooks/p1': (req: MockRequest) => {
          sent.push(req.body);
          return { playbook: editorPlaybook({ inline: (req.body as { inline: string }).inline }) };
        },
      }),
    );
    renderAt('/o/acme/playbooks/p1');
    const editor = await findCodeEditor('Playbook YAML');
    await user.click(editor);
    replaceCode(editor, 'hosts: all');
    await user.keyboard('{Control>}s{/Control}');
    expect(sent).toEqual([]);
    replaceCode(editor, '- hosts: all\n');
    await user.keyboard('{Meta>}s{/Meta}');
    await waitFor(() => expect(sent).toEqual([{ inline: '- hosts: all\n' }]));
  });

  it("shows the server's refusal in the Check panel", async () => {
    const user = userEvent.setup();
    mockApi(routes('operator', { 'PUT /api/v1/orgs/acme/playbooks/p1': () => problem(422, 'invalid input: malformed YAML: unknown anchor') }));
    renderAt('/o/acme/playbooks/p1');
    replaceCode(await findCodeEditor('Playbook YAML'), '- hosts: all\n  vars: &a\n    x: 1\n');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    const check = screen.getByRole('region', { name: 'Check' });
    expect(await within(check).findByText('invalid input: malformed YAML: unknown anchor')).toBeInTheDocument();
    expect(within(check).getByText('The server refused the last save:')).toBeInTheDocument();
    expect(screen.getByText('Unsaved')).toBeInTheDocument();
  });

  it('keeps the draft across tab switches without asking', async () => {
    const user = userEvent.setup();
    mockApi(routes());
    const { router } = renderAt('/o/acme/playbooks/p1');
    replaceCode(await findCodeEditor('Playbook YAML'), '- hosts: changed\n');
    await user.click(screen.getByRole('tab', { name: 'Runs' }));
    await waitFor(() => expect(router.state.location.search).toEqual({ tab: 'runs' }));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    await user.click(screen.getByRole('tab', { name: /Used by/ }));
    await user.click(screen.getByRole('tab', { name: 'Editor' }));
    await waitFor(() => expect(router.state.location.search).toEqual({}));
    expect(codeOf(await findCodeEditor('Playbook YAML'))).toBe('- hosts: changed\n');
    expect(screen.getByText('Unsaved')).toBeInTheDocument();
  });

  it('asks before leaving with unsaved changes, and stays when declined', async () => {
    const user = userEvent.setup();
    mockApi(routes());
    const { router } = renderAt('/o/acme/playbooks/p1');
    replaceCode(await findCodeEditor('Playbook YAML'), '- hosts: changed\n');
    await screen.findByText('Unsaved');
    void router.navigate({ to: '/o/$org/playbooks', params: { org: 'acme' } });
    expect(await declineConfirm(user)).toBe('Leave this page? Your unsaved changes will be lost.');
    expect(router.state.location.pathname).toBe('/o/acme/playbooks/p1');
    void router.navigate({ to: '/o/$org/playbooks', params: { org: 'acme' } });
    await acceptConfirm(user);
    expect(await screen.findByText('PLAYBOOKS')).toBeInTheDocument();
  });

  it('leaves a clean page without asking', async () => {
    mockApi(routes());
    const { router } = renderAt('/o/acme/playbooks/p1');
    await findCodeEditor('Playbook YAML');
    void router.navigate({ to: '/o/$org/playbooks', params: { org: 'acme' } });
    expect(await screen.findByText('PLAYBOOKS')).toBeInTheDocument();
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('gives a repository playbook no editor and no Save, but a note where it lives', async () => {
    mockApi(routes());
    renderAt('/o/acme/playbooks/p2');
    expect(await screen.findByRole('heading', { level: 1, name: 'playbooks/site.yml' })).toBeInTheDocument();
    expect(await screen.findByText('infra @ main')).toBeInTheDocument();
    const note = screen.getByRole('note', { name: 'Edited in its repository' });
    expect(note).toHaveTextContent('playbooks/site.yml lives in infra @ main. Changes come through Git');
    expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument();
    expect(screen.queryByRole('textbox', { name: 'Playbook YAML' })).not.toBeInTheDocument();
  });

  it('shows a viewer the YAML read-only, without Save', async () => {
    mockApi(routes('viewer'));
    renderAt('/o/acme/playbooks/p1');
    await heading();
    expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument();
    expect(screen.getByText('You can read this playbook; changing it needs the operator role.')).toBeInTheDocument();
    const editor = await findCodeEditor('Playbook YAML');
    expect(editor).toHaveAttribute('aria-readonly', 'true');
  });

  it('lists the variables the YAML uses as a hint', async () => {
    mockApi(
      routes('operator', {
        'GET /api/v1/orgs/acme/playbooks/p1': {
          playbook: editorPlaybook({ inline: '- hosts: all\n  vars:\n    port: 80\n  tasks:\n    - ansible.builtin.debug:\n        msg: "{{ port }} {{ domain }}"\n' }),
        },
      }),
    );
    renderAt('/o/acme/playbooks/p1');
    const panel = await screen.findByRole('region', { name: 'Variables' });
    expect(await within(panel).findByText('port')).toBeInTheDocument();
    expect(within(panel).getByText('set in vars')).toBeInTheDocument();
    expect(within(panel).getByText('domain')).toBeInTheDocument();
    expect(within(panel).getByText('needs a value from the template or inventory')).toBeInTheDocument();
    expect(within(panel).getByText(/A hint read from the YAML/)).toBeInTheDocument();
  });

  it('shows the latest runs with a way to the Runs tab', async () => {
    const user = userEvent.setup();
    const asked: URLSearchParams[] = [];
    mockApi(
      routes('operator', {
        'GET /api/v1/orgs/acme/jobs': (req: MockRequest) => {
          asked.push(req.search);
          return { jobs: [{ id: 'j1aaaaaa', kind: 'run', status: 'failed', trigger: 'manual', template_id: 't1', run_group_id: 'j1', zone_id: 'z', created_at: '2026-11-18T09:00:00Z' }], next_cursor: '' };
        },
      }),
    );
    const { router } = renderAt('/o/acme/playbooks/p1');
    const panel = await screen.findByRole('region', { name: 'Latest runs' });
    expect(await within(panel).findByText('Deploy web')).toBeInTheDocument();
    expect(asked[0].get('playbook_id')).toBe('p1');
    expect(asked[0].get('limit')).toBe('3');
    await user.click(within(panel).getByRole('button', { name: 'All runs' }));
    await waitFor(() => expect(router.state.location.search).toEqual({ tab: 'runs' }));
  });
});
