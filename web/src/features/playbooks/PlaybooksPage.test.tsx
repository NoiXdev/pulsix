import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { PlaybooksPage } from './PlaybooksPage.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';

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
  const newRoute = createRoute({ getParentRoute: () => orgRoute, path: '/playbooks/new', component: () => <div>NEW PLAYBOOK</div> });
  const pageRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/playbooks/$id',
    component: function Page() {
      return <div>PLAYBOOK {pageRoute.useParams().id}</div>;
    },
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([listRoute, newRoute, pageRoute])]),
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

  it('opens the new page from New playbook', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [] },
      'GET /api/v1/orgs/acme/repos': { repos: [] },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New playbook' }));
    expect(await screen.findByText('NEW PLAYBOOK')).toBeInTheDocument();
  });

  it('offers no New playbook to a viewer', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': { org: { ...ORG.org, role: 'viewer' } },
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbook({})] },
      'GET /api/v1/orgs/acme/repos': { repos: [] },
    });
    renderPage();
    expect(await screen.findByText('Install nginx')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'New playbook' })).not.toBeInTheDocument();
  });

  it('opens an editor and a repository playbook from its row', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/playbooks': {
        playbooks: [playbook({}), playbook({ id: 'p3', source: 'repo', repo_id: 'r1', path: 'playbooks/site.yml', summary: '' })],
      },
      'GET /api/v1/orgs/acme/repos': { repos: [repoRow] },
    });
    renderPage();
    expect(await screen.findByRole('link', { name: 'playbooks/site.yml' })).toHaveAttribute('href', '/o/acme/playbooks/p3');
    await user.click(screen.getByRole('link', { name: 'Install nginx' }));
    expect(await screen.findByText('PLAYBOOK p1')).toBeInTheDocument();
  });
});
