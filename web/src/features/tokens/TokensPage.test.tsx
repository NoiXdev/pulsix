import { describe, expect, it } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { TokensPage } from './TokensPage.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { acceptConfirm } from '../../test/confirmDialog.ts';

function renderPage(qc: QueryClient) {
  const rootRoute = createRootRoute();
  const loginRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/login',
    validateSearch: (s: Record<string, unknown>) => ({ step: typeof s.step === 'string' ? s.step : undefined }),
    component: () => <div>LOGIN PAGE</div>,
  });
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const tokensRoute = createRoute({ getParentRoute: () => orgRoute, path: '/settings/tokens', component: TokensPage });
  const routeTree = rootRoute.addChildren([loginRoute, orgRoute.addChildren([tokensRoute])]);
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ['/o/acme/settings/tokens'] }) });

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

describe('TokensPage role picker', () => {
  // v0.4g Task 9: Role is a fixed enum (RHF-registered), converted from a
  // native select to ui/Select via Controller — this is the first test that
  // actually changes the value, rather than relying on the 'viewer' default.
  it('creates a token with the picked role, not the default', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let posted: Record<string, unknown> | undefined;
    mockApi({
      'GET /api/v1/orgs/acme/tokens': { tokens: [] },
      'POST /api/v1/orgs/acme/tokens': (req: MockRequest) => {
        posted = req.body as Record<string, unknown>;
        return { id: 'tok1', name: 'ci-deploy', role: 'admin', token: 'pxe_x', created_at: '2026-01-01T00:00:00Z' };
      },
    });

    renderPage(qc);

    await user.click(await screen.findByRole('button', { name: 'Create token' }));
    await user.type(screen.getByLabelText('Name'), 'ci-deploy');
    await pickOption(screen, 'Role', 'Admin');
    const submitButtons = screen.getAllByRole('button', { name: 'Create token' });
    await user.click(submitButtons[submitButtons.length - 1]);

    expect(await screen.findByText('pxe_x')).toBeInTheDocument();
    expect(posted).toMatchObject({ name: 'ci-deploy', role: 'admin' });
  });
});

describe('TokensPage error handling (cheap fix)', () => {
  it('shows a toast instead of an unhandled rejection when revoking fails', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });

    mockApi({
      'GET /api/v1/orgs/acme/tokens': {
        tokens: [{ id: 'tok1', name: 'ci-deploy', role: 'viewer', last_used_at: null, expires_at: null }],
      },
      'DELETE /api/v1/orgs/acme/tokens/tok1': () =>
        new Response(JSON.stringify({ detail: 'token already revoked' }), { status: 409 }),
    });

    renderPage(qc);

    await user.click(await screen.findByRole('button', { name: 'revoke' }));
    await acceptConfirm(user);

    const toast = await screen.findByRole('status');
    expect(toast).toHaveTextContent('token already revoked');
  });
});

/**
 * Fix round 2 (task-15a-report.md): the previous round's own
 * `if (tokens.isError) return <QueryError .../>` regressed every wired
 * page, this one included — TanStack Query leaves `data` in place on a
 * background refetch failure (a transient 500, a window-focus refetch,
 * ...) but still flips `isError`, so the old check tore the page down on
 * a transient hiccup instead of quietly keeping the last good list up.
 * These two tests exercise the real page through `useQueryGuard`
 * (ui/useQueryGuard.tsx), not just the hook in isolation.
 */
describe('TokensPage background refetch error handling (fix round 2)', () => {
  it('keeps showing the already-loaded tokens and raises exactly one toast when a background refetch fails', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let call = 0;
    mockApi({
      'GET /api/v1/orgs/acme/tokens': () => {
        call += 1;
        if (call === 1) return { tokens: [{ id: 'tok1', name: 'ci-deploy', role: 'viewer', last_used_at: null, expires_at: null }] };
        return new Response(JSON.stringify({ detail: 'internal error' }), { status: 500 });
      },
    });

    renderPage(qc);
    await screen.findByText('ci-deploy');

    await qc.refetchQueries();

    // Still on the tokens page with the last-loaded row, not the
    // full-page error — this is exactly what regressed.
    expect(await screen.findByText('ci-deploy')).toBeInTheDocument();
    expect(screen.queryByText('Something went wrong')).not.toBeInTheDocument();

    const toasts = await screen.findAllByRole('status');
    expect(toasts).toHaveLength(1);
    expect(toasts[0]).toHaveTextContent('Could not refresh this page');
  });

  it('redirects to /login when a background refetch comes back 401, without tearing the page down first', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let call = 0;
    mockApi({
      'GET /api/v1/orgs/acme/tokens': () => {
        call += 1;
        if (call === 1) return { tokens: [{ id: 'tok1', name: 'ci-deploy', role: 'viewer', last_used_at: null, expires_at: null }] };
        return new Response(JSON.stringify({ detail: 'session expired' }), { status: 401 });
      },
    });

    renderPage(qc);
    await screen.findByText('ci-deploy');

    await qc.refetchQueries();

    expect(await screen.findByText('LOGIN PAGE')).toBeInTheDocument();
  });
});
