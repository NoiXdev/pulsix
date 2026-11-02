import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { TemplatesPage } from './TemplatesPage.tsx';

const ORG = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } };
const template = (id: string, name: string, hostIds: string[]) => ({
  id, name, playbook_id: 'pb1', scope: { group_id: null, host_ids: hostIds }, limit: '', extra_vars: {}, created_at: '', updated_at: '',
});

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const listRoute = createRoute({ getParentRoute: () => orgRoute, path: '/templates', component: TemplatesPage });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([listRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/templates'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <I18nProvider locale="de">
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe('TemplatesPage in German', () => {
  it('names the run action "Ausführen", the label the triggers hint points to', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/job-templates': { templates: [template('t1', 'deploy-site', ['h1', 'h2']), template('t2', 'patch', ['h1'])] },
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [{ id: 'pb1', source: 'editor', summary: 'Nginx installieren', created_at: '', updated_at: '' }] },
      'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [] },
    });
    renderPage();

    expect(await screen.findByText('deploy-site')).toBeInTheDocument();
    const run = screen.getByRole('button', { name: 'deploy-site ausführen' });
    expect(run).toHaveTextContent(/^Ausführen$/);
    expect(screen.getAllByText('Ausführen')).toHaveLength(2);
  });

  it('writes the page and its counts in German, with the singular for one', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/job-templates': { templates: [template('t1', 'deploy-site', ['h1', 'h2']), template('t2', 'patch', ['h1'])] },
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [] },
      'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [] },
    });
    renderPage();

    expect(await screen.findByRole('heading', { name: 'Job-Vorlagen' })).toBeInTheDocument();
    expect(screen.getByText(/^2 Vorlagen · /)).toBeInTheDocument();
    expect(screen.getByText('2 Hosts')).toBeInTheDocument();
    expect(screen.getByText('1 Host')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Vorlage hinzufügen' })).toBeInTheDocument();
  });
});
