import { describe, expect, it, vi } from 'vitest';
import userEvent from '@testing-library/user-event';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider, useQuery } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { mockApi, type MockRoute } from '../test/mockApi.ts';
import { api, unwrap } from '../api/client.ts';
import { RootLayout } from './RootLayout.tsx';
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
  const passwordRoute = createRoute({ getParentRoute: () => rootRoute, path: '/account/password', component: () => <div>CHANGE PASSWORD</div> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgsRoute, enrolRoute, passwordRoute]),
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

    it('shows the lock screen without rendering the page when /auth/me says locked', async () => {
      const orgs = vi.fn(() => ({ orgs: [] }));
      renderAt('/orgs', { 'GET /api/v1/auth/me': meBody(true), 'GET /api/v1/orgs': orgs });

      expect(await screen.findByRole('button', { name: 'Unlock' })).toBeInTheDocument();
      expect(screen.queryByText('ORGS PAGE')).not.toBeInTheDocument();
      expect(orgs).not.toHaveBeenCalled();
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
});
