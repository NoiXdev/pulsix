import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { AgentsPage } from './AgentsPage.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';

const org = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'viewer', created_at: '' } };
const agent = (n: number, status: string) => ({
  host_id: `h${n}`, host_name: `web-${n}`, agent_name: `web-${n}`, status, last_seen: new Date().toISOString(),
  version: 'v0.3a', cert_expires_at: new Date(Date.now() + 60 * 86_400_000).toISOString(), created_at: '',
});

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const listRoute = createRoute({ getParentRoute: () => orgRoute, path: '/agents', component: AgentsPage });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([listRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/agents'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <I18nProvider locale="de">
        <ConfirmProvider>
          <ToastProvider>
            <RouterProvider router={router} />
          </ToastProvider>
        </ConfirmProvider>
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe('AgentsPage in German', () => {
  it('conjugates the summary for one reporting agent', async () => {
    mockApi({ 'GET /api/v1/orgs/acme': org, 'GET /api/v1/orgs/acme/agents': { agents: [agent(1, 'online')] } });
    renderPage();

    expect(await screen.findByText(/^1 von 1 meldet sich · /)).toBeInTheDocument();
  });

  it('uses the plural verb for two reporting agents', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org,
      'GET /api/v1/orgs/acme/agents': { agents: [agent(1, 'online'), agent(2, 'online'), agent(3, 'offline')] },
    });
    renderPage();

    expect(await screen.findByText(/^2 von 3 melden sich · /)).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Agents' })).toBeInTheDocument();
  });

  it('labels the two install commands in German', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': { org: { ...org.org, role: 'operator' } },
      'GET /api/v1/orgs/acme/agents': { agents: [] },
      'GET /api/v1/orgs/acme/agents/tokens': { tokens: [] },
      'GET /api/v1/orgs/acme/zones': { zones: [{ id: 'z1', org_id: 'o', slug: 'hel', name: 'Helsinki' }] },
      'POST /api/v1/orgs/acme/agents/tokens': () => ({
        token: 'x', token_record: {}, install_command: 'curl unix', install_command_windows: 'iex win',
      }),
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: /Agent registrieren/ }));
    await user.click(await screen.findByRole('button', { name: 'Token erstellen' }));

    expect(await screen.findByLabelText('Linux und macOS')).toHaveTextContent('curl unix');
    expect(screen.getByLabelText('Windows (PowerShell als Administrator)')).toHaveTextContent('iex win');
    expect(screen.getByRole('button', { name: 'Befehl für Linux und macOS kopieren' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Befehl für Windows kopieren' })).toBeInTheDocument();
  });
});
