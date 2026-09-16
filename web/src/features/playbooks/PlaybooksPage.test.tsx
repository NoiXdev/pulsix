import { describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { PlaybooksPage } from './PlaybooksPage.tsx';

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
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
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

  it('creates an editor playbook from the YAML the textarea holds', async () => {
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
    const editor = screen.getByLabelText('Playbook YAML');
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
    await user.type(screen.getByLabelText('Playbook YAML'), 'hosts: all');
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
    await user.type(screen.getByLabelText('Playbook YAML'), '- hosts: all\n  vars: &a\n    x: 1');
    await user.click(screen.getByRole('button', { name: 'Save playbook' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('malformed YAML: unknown anchor');
  });

  it('edits an editor playbook through the YAML textarea', async () => {
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
    await user.click(await screen.findByRole('button', { name: 'Install nginx' }));
    const dialog = await screen.findByRole('dialog', { name: 'Install nginx' });
    const yaml = within(dialog).getByLabelText('Playbook YAML') as HTMLTextAreaElement;
    expect(yaml.value).toContain('hosts: all');
    await user.clear(yaml);
    await user.type(yaml, '- hosts: web');
    await user.click(within(dialog).getByRole('button', { name: 'Save playbook' }));
    expect(put).toEqual({ inline: '- hosts: web' });
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
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbook({})] },
      'GET /api/v1/orgs/acme/playbooks/p1': { playbook: playbook({ inline: '- hosts: all\n  tasks: []\n' }) },
      'GET /api/v1/orgs/acme/repos': { repos: [] },
      'DELETE /api/v1/orgs/acme/playbooks/p1': () =>
        new Response(JSON.stringify({ status: 409, detail: 'the playbook is still referenced by a job template' }), { status: 409, headers: { 'Content-Type': 'application/json' } }),
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Install nginx' }));
    const dialog = await screen.findByRole('dialog', { name: 'Install nginx' });
    await user.click(within(dialog).getByRole('button', { name: 'Delete' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('still referenced by a job template');
  });
});
