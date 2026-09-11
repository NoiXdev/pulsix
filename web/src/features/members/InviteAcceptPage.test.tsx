import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { InviteAcceptPage } from './InviteAcceptPage.tsx';

function renderPage(qc: QueryClient) {
  const rootRoute = createRootRoute();
  const inviteRoute = createRoute({ getParentRoute: () => rootRoute, path: '/invite/$token', component: InviteAcceptPage });
  const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: '/', component: () => <div>ROOT</div> });
  const routeTree = rootRoute.addChildren([indexRoute, inviteRoute]);
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ['/invite/tok_abc123'] }) });

  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

describe('InviteAcceptPage', () => {
  it('accepts the invitation and lands on the root route with a fresh session', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let accepted: unknown;
    mockApi({
      'POST /api/v1/invites/accept': (req: MockRequest) => {
        accepted = req.body;
        return { user_id: 'u2', org_id: 'o1' };
      },
      'GET /api/v1/auth/me': {
        user: { id: 'u2', email: 'ops@acme.io', display_name: 'Ops User' },
        mfa_complete: false,
        totp_confirmed: false,
        instance_admin: false,
      },
    });

    renderPage(qc);

    await user.type(await screen.findByLabelText('Your name'), 'Ops User');
    await user.type(screen.getByLabelText('Choose a password'), 'correct horse battery');
    await user.click(screen.getByRole('button', { name: 'Accept invitation' }));

    expect(await screen.findByText('ROOT')).toBeInTheDocument();
    expect(accepted).toMatchObject({ token: 'tok_abc123', display_name: 'Ops User' });
  });

  it('shows an inline error instead of an unhandled rejection when the token is invalid', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mockApi({
      'POST /api/v1/invites/accept': () => new Response(JSON.stringify({ detail: 'invitation expired' }), { status: 410 }),
    });

    renderPage(qc);

    await user.type(await screen.findByLabelText('Your name'), 'Ops User');
    await user.type(screen.getByLabelText('Choose a password'), 'correct horse battery');
    await user.click(screen.getByRole('button', { name: 'Accept invitation' }));

    expect(await screen.findByText('invitation expired')).toBeInTheDocument();
  });
});
