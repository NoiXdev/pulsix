import { describe, expect, it, vi } from 'vitest';
import userEvent from '@testing-library/user-event';
import { render, screen } from '@testing-library/react';
import { onlineManager, QueryClient, QueryClientProvider, useQuery } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { mockApi, type MockRoute } from '../test/mockApi.ts';
import { api, unwrap } from '../api/client.ts';
import { RootLayout } from './RootLayout.tsx';
import { useConfirm } from '../ui/confirmContext.ts';
import { postSessionMessage } from '../features/lock/lockChannel.ts';
import { waitFor } from '@testing-library/react';

/** A page that asks for something only a session free of the password
 * flag may have, and shows what it got. */
function OrgsPage() {
  const orgs = useQuery({ queryKey: ['orgs'], queryFn: () => unwrap(api.GET('/api/v1/orgs')) });
  return <div>{orgs.isError ? `ORGS FAILED: ${orgs.error.message}` : 'ORGS PAGE'}</div>;
}

let qcRef: QueryClient;

function renderAt(initial: string, routes: Record<string, MockRoute>) {
  mockApi(routes);
  qcRef = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute({ component: RootLayout });
  const orgsRoute = createRoute({ getParentRoute: () => rootRoute, path: '/orgs', component: OrgsPage });
  const enrolRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/account/enrol',
    component: function EnrolStub() {
      return (
        <>
          <div>ENROLMENT</div>
          <OrgsPage />
        </>
      );
    },
  });
  const loginRoute = createRoute({ getParentRoute: () => rootRoute, path: '/login', component: () => <div>LOGIN</div> });
  const passwordRoute = createRoute({ getParentRoute: () => rootRoute, path: '/account/password', component: () => <div>CHANGE PASSWORD</div> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgsRoute, enrolRoute, passwordRoute, loginRoute]),
    history: createMemoryHistory({ initialEntries: [initial] }),
  });
  render(
    <QueryClientProvider client={qcRef}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

const refusal = (detail: string) => () =>
  new Response(JSON.stringify({ title: 'Forbidden', status: 403, detail }), { status: 403 });

describe('RootLayout', () => {
  it('takes a session that must change its password to /account/password from wherever a request is refused', async () => {
    renderAt('/orgs', { 'GET /api/v1/orgs': refusal('password_change_required') });

    expect(await screen.findByText('CHANGE PASSWORD')).toBeInTheDocument();
  });

  it('leaves any other refusal to the page', async () => {
    renderAt('/orgs', { 'GET /api/v1/orgs': refusal('second factor required') });

    expect(await screen.findByText('ORGS FAILED: second factor required')).toBeInTheDocument();
    expect(screen.queryByText('CHANGE PASSWORD')).not.toBeInTheDocument();
  });

  // Enrolment completes the session before the person has saved their
  // recovery codes; a refused background request must not pull them away.
  // The enrolment page continues to the root redirect itself when done.
  it('does not take the person away from second-factor enrolment', async () => {
    const router = renderAt('/account/enrol', { 'GET /api/v1/orgs': refusal('password_change_required') });

    expect(await screen.findByText('ORGS FAILED: password_change_required')).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/account/enrol');
    expect(screen.getByText('ENROLMENT')).toBeInTheDocument();
  });

  it('offline cold load falls through to the page instead of a blank screen', async () => {
    onlineManager.setOnline(false);
    try {
      renderAt('/orgs', { 'GET /api/v1/orgs': () => Promise.reject(new TypeError('Failed to fetch')) });
      expect(await screen.findByText(/ORGS (PAGE|FAILED)/)).toBeInTheDocument();
    } finally {
      onlineManager.setOnline(true);
    }
  });

  it('shows the loading state, not a blank screen, while /auth/me is in flight', async () => {
    renderAt('/orgs', { 'GET /api/v1/auth/me': () => new Promise(() => {}) });
    expect(await screen.findByText('Loading…')).toBeInTheDocument();
  });

  describe('session lock', () => {
    const meBody = (locked: boolean) => ({ locked, lock_pin_set: true, lock_pin_prompt: false, user: { display_name: 'Tim' } });

    it('replaces the page with the lock screen after a session_locked refusal, and drops the cached page data', async () => {
      renderAt('/orgs', { 'GET /api/v1/auth/me': meBody(false) });
      // The page first renders its data; seed a cache entry the lock must drop.
      qcRef.setQueryData(['orgs', 'secret'], { secret: 1 });
      expect(await screen.findByText('ORGS PAGE')).toBeInTheDocument();

      mockApi({ 'GET /api/v1/auth/me': meBody(false), 'GET /api/v1/orgs': refusal('session_locked') });
      await qcRef.invalidateQueries({ queryKey: ['orgs'] });

      expect(await screen.findByRole('button', { name: 'Unlock' })).toBeInTheDocument();
      expect(screen.queryByText('ORGS PAGE')).not.toBeInTheDocument();
      expect(qcRef.getQueryData(['orgs', 'secret'])).toBeUndefined();
      expect(qcRef.getQueryData(['me'])).toBeDefined();
    });

    it('drops every mutation, and the PIN it carried, from the cache when the session locks', async () => {
      renderAt('/orgs', { 'GET /api/v1/auth/me': meBody(false), 'GET /api/v1/orgs': { orgs: [] } });
      expect(await screen.findByText('ORGS PAGE')).toBeInTheDocument();
      // A PIN set just before locking (default gcTime, as any mutation).
      await qcRef.getMutationCache().build(qcRef, { mutationFn: async (_pin: string) => ({ ok: true }) }).execute('4711');
      expect(qcRef.getMutationCache().getAll().some((m) => m.state.variables === '4711')).toBe(true);

      postSessionMessage({ type: 'locked' });

      expect(await screen.findByRole('button', { name: 'Unlock' })).toBeInTheDocument();
      await waitFor(() => expect(JSON.stringify(qcRef.getMutationCache().getAll().map((m) => m.state.variables))).not.toContain('4711'));
    });

    it('shows the lock screen without rendering the page when /auth/me says locked', async () => {
      const orgs = vi.fn(() => ({ orgs: [] }));
      renderAt('/orgs', { 'GET /api/v1/auth/me': meBody(true), 'GET /api/v1/orgs': orgs });

      expect(await screen.findByRole('button', { name: 'Unlock' })).toBeInTheDocument();
      expect(screen.queryByText('ORGS PAGE')).not.toBeInTheDocument();
      expect(orgs).not.toHaveBeenCalled();
    });

    it('takes an open confirm dialog away with the page when the lock arrives', async () => {
      function Asker() {
        const confirm = useConfirm();
        return <button onClick={() => void confirm({ message: 'Forget secret-item?' })}>ask</button>;
      }
      mockApi({ 'GET /api/v1/auth/me': meBody(false), 'GET /api/v1/orgs': { orgs: [] } });
      qcRef = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      const rootRoute = createRootRoute({ component: RootLayout });
      const r = createRoute({ getParentRoute: () => rootRoute, path: '/orgs', component: Asker });
      const router = createRouter({ routeTree: rootRoute.addChildren([r]), history: createMemoryHistory({ initialEntries: ['/orgs'] }) });
      render(
        <QueryClientProvider client={qcRef}>
          <RouterProvider router={router} />
        </QueryClientProvider>,
      );
      await userEvent.click(await screen.findByRole('button', { name: 'ask' }));
      expect(await screen.findByText('Forget secret-item?')).toBeInTheDocument();

      postSessionMessage({ type: 'locked' });

      expect(await screen.findByRole('button', { name: 'Unlock' })).toBeInTheDocument();
      expect(screen.queryByText('Forget secret-item?')).not.toBeInTheDocument();
      expect(screen.getByLabelText('PIN')).toBeEnabled();
    });

    it('ignores a lock signal while on a public page, and a later sign-in is not locked', async () => {
      const router = renderAt('/login', { 'GET /api/v1/auth/me': meBody(false), 'GET /api/v1/orgs': { orgs: [] } });
      await waitFor(() => expect(router.state.status).toBe('idle'));

      postSessionMessage({ type: 'locked' });
      await new Promise((r) => setTimeout(r, 50));
      router.history.push('/orgs');

      expect(await screen.findByText('ORGS PAGE')).toBeInTheDocument();
      expect(screen.queryByRole('button', { name: 'Unlock' })).not.toBeInTheDocument();
    });

    it('follows a lock another tab announces', async () => {
      renderAt('/orgs', { 'GET /api/v1/auth/me': meBody(false), 'GET /api/v1/orgs': { orgs: [] } });
      expect(await screen.findByText('ORGS PAGE')).toBeInTheDocument();

      postSessionMessage({ type: 'locked' });

      expect(await screen.findByRole('button', { name: 'Unlock' })).toBeInTheDocument();
      await waitFor(() => expect(screen.queryByText('ORGS PAGE')).not.toBeInTheDocument());
    });

    it('brings the page back after the right PIN', async () => {
      renderAt('/orgs', {
        'GET /api/v1/auth/me': meBody(true),
        'GET /api/v1/orgs': { orgs: [] },
        'POST /api/v1/auth/lock/unlock': () => {
          mockApi({ 'GET /api/v1/auth/me': meBody(false), 'GET /api/v1/orgs': { orgs: [] }, 'POST /api/v1/auth/lock/unlock': { ok: true } });
          return { ok: true };
        },
      });
      await userEvent.type(await screen.findByLabelText('PIN'), '1234');
      await userEvent.click(screen.getByRole('button', { name: 'Unlock' }));
      expect(await screen.findByText('ORGS PAGE')).toBeInTheDocument();
    });
  });
  describe('session expiry warning (backlog 3.94)', () => {
    const soon = () => new Date(Date.now() + 2 * 60 * 1000).toISOString();
    const meExpiring = (extra: Record<string, unknown>) => ({
      locked: false,
      mfa_complete: true,
      session_expires_at: soon(),
      user: { display_name: 'Tim' },
      ...extra,
    });

    it('warns an unlocked, MFA-complete session inside the last five minutes', async () => {
      renderAt('/orgs', { 'GET /api/v1/auth/me': meExpiring({}), 'GET /api/v1/orgs': { orgs: [] } });
      expect(await screen.findByRole('dialog', { name: 'Your session is about to end' })).toBeInTheDocument();
    });

    it('does not warn while the session is locked', async () => {
      renderAt('/orgs', { 'GET /api/v1/auth/me': meExpiring({ locked: true }), 'GET /api/v1/orgs': { orgs: [] } });
      expect(await screen.findByRole('button', { name: 'Unlock' })).toBeInTheDocument();
      expect(screen.queryByRole('dialog', { name: 'Your session is about to end' })).not.toBeInTheDocument();
    });

    it('does not warn a session that must change its password, which cannot renew', async () => {
      renderAt('/orgs', {
        'GET /api/v1/auth/me': meExpiring({ must_change_password: true }),
        'GET /api/v1/orgs': { orgs: [] },
      });
      await waitFor(() => expect(screen.queryByText('ORGS PAGE') ?? screen.queryByText('CHANGE PASSWORD')).toBeInTheDocument());
      expect(screen.queryByRole('dialog', { name: 'Your session is about to end' })).not.toBeInTheDocument();
    });

    it('does not warn a session that is not MFA-complete', async () => {
      renderAt('/orgs', { 'GET /api/v1/auth/me': meExpiring({ mfa_complete: false }), 'GET /api/v1/orgs': { orgs: [] } });
      expect(await screen.findByText('ORGS PAGE')).toBeInTheDocument();
      expect(screen.queryByRole('dialog', { name: 'Your session is about to end' })).not.toBeInTheDocument();
    });

    it('does not ask /auth/me on a public page, so there is nothing to warn about', async () => {
      renderAt('/login', { 'GET /api/v1/auth/me': meExpiring({}) });
      expect(await screen.findByText('LOGIN')).toBeInTheDocument();
      expect(screen.queryByRole('dialog', { name: 'Your session is about to end' })).not.toBeInTheDocument();
    });
  });
});
