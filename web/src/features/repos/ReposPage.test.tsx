import { describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { ReposPage } from './ReposPage.tsx';

const ORG = { org: { id: 'org-1', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } };
const gitKey = { id: 'c1', name: 'deploy-key', kind: 'git_key', username: 'git', expired: false, expires_soon: false, used_by: { bastions: 0, hosts: 0 }, created_at: '', updated_at: '' };
const sshKey = { id: 'c2', name: 'host-login', kind: 'ssh_key', username: 'ansible', expired: false, expires_soon: false, used_by: { bastions: 0, hosts: 3 }, created_at: '', updated_at: '' };

const repo = (over: Record<string, unknown> = {}) => ({
  id: 'r1', name: 'infra', url: 'https://git.example/acme/infra.git', branch: 'main', subpath: '',
  credential_id: 'c1', created_at: '', updated_at: '', ...over,
});

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const reposRoute = createRoute({ getParentRoute: () => orgRoute, path: '/repos', component: ReposPage });
  const jobRoute = createRoute({ getParentRoute: () => orgRoute, path: '/jobs/$id', component: () => <div>RUN VIEW</div> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([reposRoute, jobRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/repos'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('ReposPage', () => {
  it('lists repositories with url, branch, credential and sync state', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/repos': { repos: [repo({ last_sync: '2026-09-15T10:00:00Z' })] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey, sshKey] },
    });
    renderPage();
    expect(await screen.findByText('infra')).toBeInTheDocument();
    expect(screen.getByText('https://git.example/acme/infra.git')).toBeInTheDocument();
    expect(screen.getByText('main')).toBeInTheDocument();
    expect(await screen.findByText(/deploy-key/)).toBeInTheDocument();
    expect(screen.getByText(/^synced 2026-09-15/)).toBeInTheDocument();
  });

  it('marks a failed sync in crit with its error as the title', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/repos': { repos: [repo({ last_sync: '2026-09-15T10:00:00Z', last_sync_error: 'authentication failed' })] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey] },
    });
    renderPage();
    const pill = await screen.findByText('failed');
    expect(pill).toHaveStyle({ color: 'var(--px-crit)' });
    expect(screen.getByTitle('authentication failed')).toBeInTheDocument();
  });

  it('warns for a repository that has never been synced', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/repos': { repos: [repo()] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey] },
    });
    renderPage();
    expect(await screen.findByText('never synced')).toBeInTheDocument();
  });

  it('creates a repository and offers git credentials only', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    const repos: Record<string, unknown>[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/repos': () => ({ repos }),
      'POST /api/v1/orgs/acme/repos': (req: MockRequest) => {
        posted = req.body;
        const created = repo({ id: 'r2', name: 'new-repo' });
        repos.push(created);
        return { repo: created };
      },
      'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey, sshKey] },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add repository' }));
    await user.type(screen.getByLabelText('Name'), 'new-repo');
    await user.type(screen.getByLabelText('URL'), 'https://git.example/acme/new.git');
    await user.selectOptions(screen.getByLabelText('Credential'), 'c1');

    // The credential select offered the git key, not the SSH key.
    const select = screen.getByLabelText('Credential') as HTMLSelectElement;
    const options = [...select.options].filter((o) => o.value !== '').map((o) => o.value);
    expect(options).toEqual(['c1']);

    await user.click(screen.getByRole('button', { name: 'Create repository' }));
    expect(await screen.findByText('new-repo')).toBeInTheDocument();
    expect(posted).toEqual({ name: 'new-repo', url: 'https://git.example/acme/new.git', branch: 'main', subpath: '', credential_id: 'c1' });
  });

  it('shows the server-mirror validation message at the field without a round trip', async () => {
    const user = userEvent.setup();
    const posted: unknown[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/repos': { repos: [] },
      'POST /api/v1/orgs/acme/repos': (req: MockRequest) => {
        posted.push(req.body);
        return { repo: repo() };
      },
      'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey] },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add repository' }));
    await user.type(screen.getByLabelText('Name'), 'Bad Name');
    await user.type(screen.getByLabelText('URL'), 'https://git.example/acme/new.git');
    await user.click(screen.getByRole('button', { name: 'Create repository' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('invalid name: must be lowercase letters');
    expect(posted).toEqual([]);
  });

  it('shows a server 409 (name taken) at the name field', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/repos': { repos: [] },
      'POST /api/v1/orgs/acme/repos': () =>
        new Response(JSON.stringify({ status: 409, detail: 'name already in use' }), { status: 409, headers: { 'Content-Type': 'application/json' } }),
      'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey] },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add repository' }));
    await user.type(screen.getByLabelText('Name'), 'infra');
    await user.type(screen.getByLabelText('URL'), 'https://git.example/acme/infra.git');
    await user.click(screen.getByRole('button', { name: 'Create repository' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('name already in use');
  });

  it('syncs a repository and links to the run view', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/repos': { repos: [repo()] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey] },
      'POST /api/v1/orgs/acme/repos/r1/sync': { job: { id: 'job-1', zone_id: 'z1', kind: 'sync_repo', status: 'queued', run_group_id: 'g1', trigger: 'manual', created_at: '' } },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Sync infra' }));
    expect(await screen.findByText('RUN VIEW')).toBeInTheDocument();
  });

  it('edits a repository through the dialog', async () => {
    const user = userEvent.setup();
    let put: unknown;
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/repos': { repos: [repo()] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey] },
      'PUT /api/v1/orgs/acme/repos/r1': (req: MockRequest) => {
        put = req.body;
        return { repo: repo({ branch: 'release' }) };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'infra' }));
    const dialog = await screen.findByRole('dialog', { name: 'infra' });
    const branch = within(dialog).getByLabelText('Branch');
    await user.clear(branch);
    await user.type(branch, 'release');
    await user.click(within(dialog).getByRole('button', { name: 'Save repository' }));
    expect(put).toEqual({ name: 'infra', url: 'https://git.example/acme/infra.git', branch: 'release', subpath: '', credential_id: 'c1' });
  });

  it('deletes a repository after confirmation', async () => {
    const user = userEvent.setup();
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    let deleted = false;
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/repos': () => ({ repos: deleted ? [] : [repo()] }),
      'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey] },
      'DELETE /api/v1/orgs/acme/repos/r1': () => {
        deleted = true;
        return {};
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'infra' }));
    const dialog = await screen.findByRole('dialog', { name: 'infra' });
    await user.click(within(dialog).getByRole('button', { name: 'Delete' }));
    expect(await screen.findByText('No repositories yet.')).toBeInTheDocument();
  });
});
