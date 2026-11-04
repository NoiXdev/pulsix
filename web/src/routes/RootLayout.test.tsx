import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider, useQuery } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { mockApi, type MockRoute } from '../test/mockApi.ts';
import { api, unwrap } from '../api/client.ts';
import { RootLayout } from './RootLayout.tsx';

/** A page that asks for something only a session free of the password
 * flag may have, and shows what it got. */
function OrgsPage() {
  const orgs = useQuery({ queryKey: ['orgs'], queryFn: () => unwrap(api.GET('/api/v1/orgs')) });
  return <div>{orgs.isError ? `ORGS FAILED: ${orgs.error.message}` : 'ORGS PAGE'}</div>;
}

function renderAt(initial: string, routes: Record<string, MockRoute>) {
  mockApi(routes);
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
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
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
});
