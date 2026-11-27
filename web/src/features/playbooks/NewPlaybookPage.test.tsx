import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { declineConfirm } from '../../test/confirmDialog.ts';
import { codeOf, findCodeEditor, replaceCode } from '../../test/codeEditor.ts';
import { pickOption } from '../../test/pickOption.ts';
import { PLAYBOOK_STARTERS } from './starters.ts';
import { editorPlaybook, repoPlaybook, renderAt, routes } from './playbookPageTestKit.tsx';

const starter = (id: string) => PLAYBOOK_STARTERS.find((s) => s.id === id)!.yaml;

const DISCOVERED = {
  playbooks: [
    { path: 'playbooks/site.yml', playbook_id: 'p2', fields: 2, failed: false, left_out: false, warnings: 0 },
    { path: 'playbooks/db.yml', fields: 0, failed: false, left_out: false, warnings: 0 },
  ],
  sources: { commit: 'abcdef1234', synced_at: '2026-11-18T08:00:00Z', incomplete: false, left_out: [], notes: [] },
};

describe('NewPlaybookPage', () => {
  it('fills the editor from a starter, names the playbook from it and asks before replacing edited text', async () => {
    const user = userEvent.setup();
    mockApi(routes());
    renderAt('/o/acme/playbooks/new');
    expect(await screen.findByRole('heading', { level: 1, name: 'New playbook' })).toBeInTheDocument();
    const editor = await findCodeEditor('Playbook YAML');
    await user.click(screen.getByRole('button', { name: /Install packages/ }));
    expect(codeOf(editor)).toBe(starter('packages'));
    expect(screen.getByText('Install packages', { selector: 'output' })).toBeInTheDocument();
    // Another starter over an untouched one replaces it at once.
    await user.click(screen.getByRole('button', { name: /Restart a service/ }));
    expect(codeOf(editor)).toBe(starter('service'));
    // Over edited text it asks first.
    replaceCode(editor, '- hosts: mine\n');
    await user.click(screen.getByRole('button', { name: /Empty play/ }));
    let ask = await screen.findByRole('dialog', { name: "Replace the editor's text?" });
    await user.click(within(ask).getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(codeOf(editor)).toBe('- hosts: mine\n');
    await user.click(screen.getByRole('button', { name: /File from a template/ }));
    ask = await screen.findByRole('dialog', { name: "Replace the editor's text?" });
    await user.click(within(ask).getByRole('button', { name: 'Replace' }));
    expect(codeOf(editor)).toBe(starter('template'));
  });

  it('creates an editor playbook and opens its page', async () => {
    const user = userEvent.setup();
    const posted: unknown[] = [];
    mockApi(
      routes('operator', {
        'POST /api/v1/orgs/acme/playbooks': (req: MockRequest) => {
          posted.push(req.body);
          return { playbook: editorPlaybook() };
        },
      }),
    );
    const { router } = renderAt('/o/acme/playbooks/new');
    const editor = await findCodeEditor('Playbook YAML');
    const create = screen.getByRole('button', { name: 'Create' });
    expect(create).toBeDisabled();
    replaceCode(editor, 'hosts: all');
    expect(await screen.findByText('expected a list of plays')).toBeInTheDocument();
    expect(create).toBeDisabled();
    replaceCode(editor, '- hosts: all\n');
    await waitFor(() => expect(create).toBeEnabled());
    await user.click(create);
    await waitFor(() => expect(router.state.location.pathname).toBe('/o/acme/playbooks/p1'));
    expect(posted).toEqual([{ inline: '- hosts: all\n' }]);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it("shows the server's refusal and stays", async () => {
    const user = userEvent.setup();
    mockApi(
      routes('operator', {
        'POST /api/v1/orgs/acme/playbooks': () =>
          new Response(JSON.stringify({ status: 400, detail: 'invalid input: malformed YAML: unknown anchor' }), { status: 400, headers: { 'Content-Type': 'application/json' } }),
      }),
    );
    const { router } = renderAt('/o/acme/playbooks/new');
    replaceCode(await findCodeEditor('Playbook YAML'), '- hosts: all\n  vars: &a\n    x: 1\n');
    await user.click(screen.getByRole('button', { name: 'Create' }));
    const check = screen.getByRole('region', { name: 'Check' });
    expect(await within(check).findByText('invalid input: malformed YAML: unknown anchor')).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/o/acme/playbooks/new');
  });

  it('asks before leaving with text in the editor', async () => {
    const user = userEvent.setup();
    mockApi(routes());
    const { router } = renderAt('/o/acme/playbooks/new');
    replaceCode(await findCodeEditor('Playbook YAML'), '- hosts: all\n');
    await user.click(screen.getByRole('link', { name: 'Cancel' }));
    expect(await declineConfirm(user)).toBe('Leave this page? Your unsaved changes will be lost.');
    expect(router.state.location.pathname).toBe('/o/acme/playbooks/new');
  });

  it('still asks before leaving with edited text after switching to a repository', async () => {
    const user = userEvent.setup();
    mockApi(routes());
    const { router } = renderAt('/o/acme/playbooks/new');
    replaceCode(await findCodeEditor('Playbook YAML'), '- hosts: all\n');
    await user.click(screen.getByRole('radio', { name: /Choose from a repository/ }));
    await user.click(screen.getByRole('link', { name: 'Cancel' }));
    expect(await declineConfirm(user)).toBe('Leave this page? Your unsaved changes will be lost.');
    expect(router.state.location.pathname).toBe('/o/acme/playbooks/new');
  });

  it('tells someone without write access that the page is not theirs', async () => {
    mockApi(routes('viewer'));
    renderAt('/o/acme/playbooks/new');
    expect(await screen.findByText('You do not have access')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Create' })).not.toBeInTheDocument();
  });

  it('adds a playbook from a repository and opens its page', async () => {
    const user = userEvent.setup();
    const picked: unknown[] = [];
    mockApi(
      routes('operator', {
        'GET /api/v1/orgs/acme/repos/r1/playbooks': DISCOVERED,
        'POST /api/v1/orgs/acme/repos/r1/playbooks': (req: MockRequest) => {
          picked.push(req.body);
          return { created: true, playbook: repoPlaybook({ id: 'p7', path: 'playbooks/db.yml' }) };
        },
      }),
    );
    const { router } = renderAt('/o/acme/playbooks/new');
    await user.click(await screen.findByRole('radio', { name: /Choose from a repository/ }));
    expect(screen.queryByRole('textbox', { name: 'Playbook YAML' })).not.toBeInTheDocument();
    const create = screen.getByRole('button', { name: 'Create' });
    expect(create).toBeDisabled();
    await pickOption(screen, 'Repository', 'infra');
    await pickOption(screen, 'Playbook', 'playbooks/db.yml');
    await user.click(create);
    await waitFor(() => expect(router.state.location.pathname).toBe('/o/acme/playbooks/p7'));
    expect(picked).toEqual([{ path: 'playbooks/db.yml' }]);
  });

  it('opens the page of a repository playbook that was already added', async () => {
    const user = userEvent.setup();
    const picked: unknown[] = [];
    mockApi(
      routes('operator', {
        'GET /api/v1/orgs/acme/repos/r1/playbooks': DISCOVERED,
        'POST /api/v1/orgs/acme/repos/r1/playbooks': (req: MockRequest) => {
          picked.push(req.body);
          return { created: false, playbook: repoPlaybook() };
        },
      }),
    );
    const { router } = renderAt('/o/acme/playbooks/new');
    await user.click(await screen.findByRole('radio', { name: /Choose from a repository/ }));
    await pickOption(screen, 'Repository', 'infra');
    await pickOption(screen, 'Playbook', 'playbooks/site.yml');
    await user.click(screen.getByRole('button', { name: 'Create' }));
    await waitFor(() => expect(router.state.location.pathname).toBe('/o/acme/playbooks/p2'));
    expect(await screen.findByText('This playbook was already added – here is its page.')).toBeInTheDocument();
  });

  it('points to adding a repository when there is none', async () => {
    const user = userEvent.setup();
    mockApi(routes('operator', { 'GET /api/v1/orgs/acme/repos': { repos: [] } }));
    renderAt('/o/acme/playbooks/new');
    await user.click(await screen.findByRole('radio', { name: /Choose from a repository/ }));
    expect(await screen.findByText('No repositories yet.')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Add a repository' })).toHaveAttribute('href', '/o/acme/repos');
  });
});
