import { describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { PlaybooksPage } from './PlaybooksPage.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { chooseRowAction } from '../../test/wholeList.ts';
import { acceptConfirm } from '../../test/confirmDialog.ts';
import { codeOf, findCodeEditor, replaceCode } from '../../test/codeEditor.ts';

const ORG = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } };
const repoRow = { id: 'r1', name: 'infra', url: 'https://git.example/acme/infra.git', branch: 'main', subpath: '', created_at: '', updated_at: '' };

const playbook = (over: Record<string, unknown> = {}) => ({
  id: 'p1', source: 'editor', summary: 'Install nginx', created_at: '', updated_at: '', ...over,
});

function renderPage(initial = '/o/acme/playbooks') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const listRoute = createRoute({ getParentRoute: () => orgRoute, path: '/playbooks', component: PlaybooksPage });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([listRoute])]),
    history: createMemoryHistory({ initialEntries: [initial] }),
  });
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

describe('PlaybooksPage', () => {
  it('lists playbooks with a source badge, summary and repo', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/playbooks': {
        playbooks: [
          playbook({}),
          playbook({ id: 'p2', source: 'repo', repo_id: 'r1', path: 'playbooks/site.yml', summary: '' }),
        ],
      },
      'GET /api/v1/orgs/acme/repos': { repos: [repoRow] },
    });
    renderPage();
    expect(await screen.findByText('Install nginx')).toBeInTheDocument();
    expect(screen.getByText('editor')).toBeInTheDocument();
    expect(screen.getByText('repo')).toBeInTheDocument();
    expect(screen.getByText('playbooks/site.yml')).toBeInTheDocument();
    expect(screen.getByText('infra')).toBeInTheDocument();
  });

  it('creates an editor playbook from the YAML the editor holds', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    const playbooks: Record<string, unknown>[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/playbooks': () => ({ playbooks }),
      'GET /api/v1/orgs/acme/repos': { repos: [] },
      'POST /api/v1/orgs/acme/playbooks': (req: MockRequest) => {
        posted = req.body;
        const created = playbook({ id: 'p2', summary: 'Install nginx' });
        playbooks.push(created);
        return { playbook: created };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New editor playbook' }));
    const editor = await findCodeEditor('Playbook YAML');
    await user.click(editor);
    await user.type(editor, '- hosts: all');
    expect(screen.getByText('Parses as a list of plays — ready to save.')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Save playbook' }));
    expect(posted).toEqual({ inline: '- hosts: all' });
    expect(await screen.findByText('Install nginx')).toBeInTheDocument();
  });

  it('shows the client-side preview message before submitting', async () => {
    const user = userEvent.setup();
    const posted: unknown[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [] },
      'GET /api/v1/orgs/acme/repos': { repos: [] },
      'POST /api/v1/orgs/acme/playbooks': (req: MockRequest) => {
        posted.push(req.body);
        return { playbook: playbook() };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New editor playbook' }));
    const editor = await findCodeEditor('Playbook YAML');
    await user.click(editor);
    await user.type(editor, 'hosts: all');
    expect(await screen.findByRole('alert')).toHaveTextContent('expected a list of plays');
    expect(posted).toEqual([]);
  });

  it('shows the server 422 message inline when the client preview passes', async () => {
    const user = userEvent.setup();
    // An anchor is outside the client subset, so only the server can judge it.
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [] },
      'GET /api/v1/orgs/acme/repos': { repos: [] },
      'POST /api/v1/orgs/acme/playbooks': () =>
        new Response(JSON.stringify({ status: 400, detail: 'invalid input: malformed YAML: unknown anchor' }), { status: 400, headers: { 'Content-Type': 'application/json' } }),
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New editor playbook' }));
    replaceCode(await findCodeEditor('Playbook YAML'), '- hosts: all\n  vars: &a\n    x: 1');
    await user.click(screen.getByRole('button', { name: 'Save playbook' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('malformed YAML: unknown anchor');
  });

  it('edits an editor playbook through the YAML editor', async () => {
    const user = userEvent.setup();
    let put: unknown;
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbook({})] },
      'GET /api/v1/orgs/acme/playbooks/p1': { playbook: playbook({ inline: '- hosts: all\n  tasks: []\n' }) },
      'GET /api/v1/orgs/acme/repos': { repos: [repoRow] },
      'PUT /api/v1/orgs/acme/playbooks/p1': (req: MockRequest) => {
        put = req.body;
        return { playbook: playbook({ summary: 'Install nginx v2' }) };
      },
    });
    renderPage();
    await chooseRowAction(user, 'Install nginx', 'Edit');
    const dialog = await screen.findByRole('dialog', { name: 'Install nginx' });
    const yaml = await findCodeEditor('Playbook YAML', within(dialog));
    expect(codeOf(yaml)).toContain('hosts: all');
    await user.click(yaml);
    await user.keyboard('{Control>}a{/Control}{Delete}');
    await user.type(yaml, '- hosts: web');
    await user.click(within(dialog).getByRole('button', { name: 'Save playbook' }));
    expect(put).toEqual({ inline: '- hosts: web' });
  });

  it('marks the line the preview names in the editor', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [] },
      'GET /api/v1/orgs/acme/repos': { repos: [] },
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New editor playbook' }));
    const dialog = await screen.findByRole('dialog', { name: 'New editor playbook' });
    replaceCode(await findCodeEditor('Playbook YAML', within(dialog)), '- hosts: all\n\ttasks: []');
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('malformed YAML: found character that cannot start any token');
    await waitFor(() => expect(dialog.querySelector('.cm-lintRange-error')?.textContent).toBe('\ttasks: []'));
  });

  it('saves with Ctrl-S once the preview passes, and not before', async () => {
    const user = userEvent.setup();
    const posted: unknown[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [] },
      'GET /api/v1/orgs/acme/repos': { repos: [] },
      'POST /api/v1/orgs/acme/playbooks': (req: MockRequest) => {
        posted.push(req.body);
        return { playbook: playbook() };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New editor playbook' }));
    const editor = await findCodeEditor('Playbook YAML');
    await user.click(editor);
    await user.type(editor, 'hosts: all');
    await user.keyboard('{Control>}s{/Control}');
    expect(posted).toEqual([]);
    replaceCode(editor, '- hosts: all');
    await user.keyboard('{Control>}s{/Control}');
    await waitFor(() => expect(posted).toEqual([{ inline: '- hosts: all' }]));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'New editor playbook' })).not.toBeInTheDocument());
  });

  it('asks before closing with unsaved changes, and closes untouched at once', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbook({})] },
      'GET /api/v1/orgs/acme/playbooks/p1': { playbook: playbook({ inline: '- hosts: all\n' }) },
      'GET /api/v1/orgs/acme/repos': { repos: [] },
    });
    renderPage();
    await chooseRowAction(user, 'Install nginx', 'Edit');
    let dialog = await screen.findByRole('dialog', { name: 'Install nginx' });
    await findCodeEditor('Playbook YAML', within(dialog));
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByRole('dialog', { name: 'Install nginx' })).not.toBeInTheDocument();
    expect(screen.queryByRole('dialog', { name: 'Discard changes?' })).not.toBeInTheDocument();

    await chooseRowAction(user, 'Install nginx', 'Edit');
    dialog = await screen.findByRole('dialog', { name: 'Install nginx' });
    replaceCode(await findCodeEditor('Playbook YAML', within(dialog)), '- hosts: web\n');
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    const question = await screen.findByRole('dialog', { name: 'Discard changes?' });
    await user.click(within(question).getByRole('button', { name: 'Cancel' }));
    expect(screen.getByRole('dialog', { name: 'Install nginx' })).toBeInTheDocument();

    // Escape asks the same (from outside the editor: inside, the first
    // Escape releases Tab).
    within(dialog).getByRole('button', { name: 'Cancel' }).focus();
    await user.keyboard('{Escape}');
    const again = await screen.findByRole('dialog', { name: 'Discard changes?' });
    await user.click(within(again).getByRole('button', { name: 'Discard' }));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Install nginx' })).not.toBeInTheDocument());
  });

  it('keeps the dialog on a first Escape in the editor (it releases Tab) and closes on a second', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [] },
      'GET /api/v1/orgs/acme/repos': { repos: [] },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New editor playbook' }));
    const editor = await findCodeEditor('Playbook YAML');
    await user.click(editor);
    await user.keyboard('{Escape}');
    expect(screen.getByRole('dialog', { name: 'New editor playbook' })).toBeInTheDocument();
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'New editor playbook' })).not.toBeInTheDocument());
  });

  it('switches the editor to full screen and back, Escape leaving full screen without a close request', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [] },
      'GET /api/v1/orgs/acme/repos': { repos: [] },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New editor playbook' }));
    const dialog = await screen.findByRole('dialog', { name: 'New editor playbook' });
    // A refused close request spends the browser's activation, after which
    // the next one cannot be refused: leaving full screen must not use one.
    let closeRequests = 0;
    dialog.addEventListener('cancel', () => closeRequests++);
    replaceCode(await findCodeEditor('Playbook YAML', within(dialog)), '- hosts: all');
    const toggle = within(dialog).getByRole('button', { name: 'Full screen' });
    expect(toggle).toHaveAttribute('aria-pressed', 'false');
    await user.click(toggle);
    expect(toggle).toHaveAttribute('aria-pressed', 'true');
    expect(within(dialog).getByRole('form', { name: 'New editor playbook' })).toHaveAttribute('data-fullscreen', 'true');
    await user.keyboard('{Escape}');
    expect(toggle).toHaveAttribute('aria-pressed', 'false');
    expect(closeRequests).toBe(0);
    // The next Escape still asks before the edit is dropped.
    await user.keyboard('{Escape}');
    expect(await screen.findByRole('dialog', { name: 'Discard changes?' })).toBeInTheDocument();
    expect(screen.getByRole('dialog', { name: 'New editor playbook' })).toBeInTheDocument();
  });

  it('opens with the focus in the YAML', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [] },
      'GET /api/v1/orgs/acme/repos': { repos: [] },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New editor playbook' }));
    const editor = await findCodeEditor('Playbook YAML');
    await waitFor(() => expect(editor).toHaveFocus());
  });

  it('gives a repo playbook no editor: it is edited in its repository', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/playbooks': {
        playbooks: [playbook({ id: 'p3', source: 'repo', repo_id: 'r1', path: 'playbooks/site.yml' })],
      },
      'GET /api/v1/orgs/acme/repos': { repos: [repoRow] },
    });
    renderPage();
    expect(await screen.findByText('playbooks/site.yml')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'playbooks/site.yml' })).not.toBeInTheDocument();
  });

  it('deletes an unused playbook after confirmation and reports a referenced one', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbook({})] },
      'GET /api/v1/orgs/acme/playbooks/p1': { playbook: playbook({ inline: '- hosts: all\n  tasks: []\n' }) },
      'GET /api/v1/orgs/acme/repos': { repos: [] },
      'DELETE /api/v1/orgs/acme/playbooks/p1': () =>
        new Response(JSON.stringify({ status: 409, detail: 'the playbook is still referenced by a job template' }), { status: 409, headers: { 'Content-Type': 'application/json' } }),
    });
    renderPage();
    await chooseRowAction(user, 'Install nginx', 'Edit');
    const dialog = await screen.findByRole('dialog', { name: 'Install nginx' });
    await user.click(within(dialog).getByRole('button', { name: 'Delete' }));
    await acceptConfirm(user);
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('still referenced by a job template');
  });
});
