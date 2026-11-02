import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { TokensPage } from './TokensPage.tsx';

describe('TokensPage in German', () => {
  it('renders the page, its tab strip and its dates in German', async () => {
    mockApi({
      'GET /api/v1/auth/me': {
        user: { id: 'u1', email: 'a@acme.io', display_name: 'A' },
        mfa_complete: true,
        mfa_enrolment_required: false,
        totp_confirmed: true,
        instance_admin: false,
      },
      'GET /api/v1/orgs/acme': { org: { id: 'o1', slug: 'acme', name: 'Acme', role: 'owner' } },
      'GET /api/v1/orgs/acme/tokens': {
        tokens: [
          { id: 'tok1', name: 'ci-deploy', role: 'viewer', last_used_at: '2026-03-04T10:00:00Z', expires_at: null },
        ],
      },
    });
    const rootRoute = createRootRoute();
    const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
    const tokensRoute = createRoute({ getParentRoute: () => orgRoute, path: '/settings/tokens', component: TokensPage });
    const router = createRouter({
      routeTree: rootRoute.addChildren([orgRoute.addChildren([tokensRoute])]),
      history: createMemoryHistory({ initialEntries: ['/o/acme/settings/tokens'] }),
    });

    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <I18nProvider locale="de">
          <ToastProvider>
            <RouterProvider router={router} />
          </ToastProvider>
        </I18nProvider>
      </QueryClientProvider>,
    );

    expect(await screen.findByRole('button', { name: 'Token erstellen' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'API-Tokens' })).toBeInTheDocument();
    expect(await screen.findByRole('tab', { name: 'Mitglieder' })).toBeInTheDocument();
    expect(screen.getByText('4.3.2026')).toBeInTheDocument();
    expect(screen.getByText('nie')).toBeInTheDocument();
  });
});
