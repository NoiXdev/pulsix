import { describe, expect, it } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { TokensPage } from './TokensPage.tsx';

function renderPage(qc: QueryClient) {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const tokensRoute = createRoute({ getParentRoute: () => orgRoute, path: '/settings/tokens', component: TokensPage });
  const routeTree = rootRoute.addChildren([orgRoute.addChildren([tokensRoute])]);
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ['/o/acme/settings/tokens'] }) });

  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

/** Critical fix (task-15a-report.md fix round 1): a freshly created API
 * token's plaintext — the API never persists it and never returns it
 * again — used to live in `mutation.data` with no gcTime override, so
 * TanStack Query's mutation cache kept it reachable for five minutes
 * after the create dialog closed, even though the dialog's own state had
 * already been cleared. */
describe('TokensPage token plaintext handling (Critical fix)', () => {
  it('leaves no mutation cache entry carrying the plaintext token once the create dialog is dismissed', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mockApi({
      'GET /api/v1/orgs/acme/tokens': { tokens: [] },
      'POST /api/v1/orgs/acme/tokens': {
        id: 'tok1',
        name: 'ci-deploy',
        role: 'viewer',
        token: 'pxe_super-secret-plaintext-token',
        created_at: '2026-01-01T00:00:00Z',
      },
    });

    renderPage(qc);

    await user.click(await screen.findByRole('button', { name: 'Create token' }));
    await user.type(screen.getByLabelText('Name'), 'ci-deploy');
    // Two "Create token" buttons exist at this point: the one that opened
    // the dialog, and the dialog's own submit button.
    const submitButtons = screen.getAllByRole('button', { name: 'Create token' });
    await user.click(submitButtons[submitButtons.length - 1]);

    expect(await screen.findByText('pxe_super-secret-plaintext-token')).toBeInTheDocument();

    const mutationsBeforeDismiss = qc.getMutationCache().getAll();
    expect(
      mutationsBeforeDismiss.some((m) => JSON.stringify(m.state.data).includes('pxe_super-secret-plaintext-token')),
    ).toBe(true);

    await user.click(screen.getByRole('button', { name: 'Done' }));

    await waitFor(() => {
      const mutations = qc.getMutationCache().getAll();
      expect(mutations.some((m) => JSON.stringify(m.state.data).includes('pxe_super-secret-plaintext-token'))).toBe(
        false,
      );
    });
    expect(screen.queryByText('pxe_super-secret-plaintext-token')).not.toBeInTheDocument();
  });
});

describe('TokensPage error handling (cheap fix)', () => {
  it('shows a toast instead of an unhandled rejection when revoking fails', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const originalConfirm = window.confirm;
    window.confirm = () => true;

    mockApi({
      'GET /api/v1/orgs/acme/tokens': {
        tokens: [{ id: 'tok1', name: 'ci-deploy', role: 'viewer', last_used_at: null, expires_at: null }],
      },
      'DELETE /api/v1/orgs/acme/tokens/tok1': () =>
        new Response(JSON.stringify({ detail: 'token already revoked' }), { status: 409 }),
    });

    renderPage(qc);

    await user.click(await screen.findByRole('button', { name: 'revoke' }));

    const toast = await screen.findByRole('status');
    expect(toast).toHaveTextContent('token already revoked');

    window.confirm = originalConfirm;
  });
});
