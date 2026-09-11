import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { MembersPage } from './MembersPage.tsx';

const MEMBERS = {
  members: [
    { user_id: 'u1', display_name: 'Tim R.', email: 'tim@acme.io', role: 'owner', created_at: '2026-01-01T00:00:00Z', disabled_at: null },
    { user_id: 'u2', display_name: 'Sara K.', email: 'sara@acme.io', role: 'viewer', created_at: '2026-02-01T00:00:00Z', disabled_at: null },
  ],
};

function renderPage(qc: QueryClient) {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const membersRoute = createRoute({ getParentRoute: () => orgRoute, path: '/settings/members', component: MembersPage });
  const routeTree = rootRoute.addChildren([orgRoute.addChildren([membersRoute])]);
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ['/o/acme/settings/members'] }) });

  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('MembersPage', () => {
  it('lists members and sends an invitation', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let invited: unknown;
    mockApi({
      'GET /api/v1/orgs/acme/members': MEMBERS,
      'POST /api/v1/orgs/acme/invites': (req: MockRequest) => {
        invited = req.body;
        return { email: 'ops@acme.io', role: 'operator', expires_at: '2026-01-08T00:00:00Z', mail_sent: true };
      },
    });

    renderPage(qc);

    expect(await screen.findByText('Tim R.')).toBeInTheDocument();
    expect(screen.getByText('Sara K.')).toBeInTheDocument();
    expect(screen.getByText('2 members')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Invite member' }));
    await user.type(screen.getByLabelText('E-mail address'), 'ops@acme.io');
    await user.click(screen.getByRole('button', { name: 'Send invitation' }));

    expect(await screen.findByText('Invitation sent to ops@acme.io')).toBeInTheDocument();
    expect(invited).toMatchObject({ email: 'ops@acme.io', role: 'operator' });
  });

  // I4: on an instance without outgoing mail the invitation is still
  // created, and the page hands the acceptance link to the administrator
  // (with a copy action) instead of claiming an e-mail went out.
  it('shows the acceptance link with a copy action when no e-mail could be sent', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const link = 'http://localhost:3000/invite/tok_nomail';
    mockApi({
      'GET /api/v1/orgs/acme/members': MEMBERS,
      'POST /api/v1/orgs/acme/invites': {
        email: 'ops@acme.io',
        role: 'operator',
        expires_at: '2026-01-08T00:00:00Z',
        mail_sent: false,
        accept_url: link,
      },
    });

    renderPage(qc);
    await user.click(await screen.findByRole('button', { name: 'Invite member' }));
    await user.type(screen.getByLabelText('E-mail address'), 'ops@acme.io');
    await user.click(screen.getByRole('button', { name: 'Send invitation' }));

    expect(await screen.findByLabelText('Invitation link')).toHaveTextContent(link);
    expect(screen.getByText(/no e-mail was sent/)).toBeInTheDocument();
    expect(screen.queryByText('Invitation sent to ops@acme.io')).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Copy link' }));
    expect(await screen.findByText('Invitation link copied')).toBeInTheDocument();
    expect(await navigator.clipboard.readText()).toBe(link);

    await user.click(screen.getByRole('button', { name: 'Done' }));
    expect(screen.queryByLabelText('Invitation link')).not.toBeInTheDocument();
  });

  it('reports a failed delivery with the server\'s explanation', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mockApi({
      'GET /api/v1/orgs/acme/members': MEMBERS,
      'POST /api/v1/orgs/acme/invites': () =>
        new Response(JSON.stringify({ detail: 'the invitation e-mail could not be delivered' }), { status: 502 }),
    });

    renderPage(qc);
    await user.click(await screen.findByRole('button', { name: 'Invite member' }));
    await user.type(screen.getByLabelText('E-mail address'), 'ops@acme.io');
    await user.click(screen.getByRole('button', { name: 'Send invitation' }));

    expect(await screen.findByText('the invitation e-mail could not be delivered')).toBeInTheDocument();
  });

  it('changes a member\'s role', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let changedTo: string | undefined;
    mockApi({
      'GET /api/v1/orgs/acme/members': MEMBERS,
      'PATCH /api/v1/orgs/acme/members/u2': (req: MockRequest) => {
        changedTo = (req.body as { role: string }).role;
        return {};
      },
    });

    renderPage(qc);

    const saraName = await screen.findByText('Sara K.');
    const saraRow = saraName.closest('div[class*="grid items-center"]') as HTMLElement;
    await user.click(within(saraRow).getByText('viewer'));

    const roleDialog = (await screen.findByText('Change role · Sara K.')).closest('dialog') as HTMLElement;
    await user.click(within(roleDialog).getByRole('button', { name: /operator/ }));

    expect(await screen.findByText('Role updated to operator')).toBeInTheDocument();
    expect(changedTo).toBe('operator');
  });

  it('removes a member after confirmation', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const originalConfirm = window.confirm;
    window.confirm = () => true;
    mockApi({
      'GET /api/v1/orgs/acme/members': MEMBERS,
      'DELETE /api/v1/orgs/acme/members/u2': {},
    });

    renderPage(qc);

    const removeButtons = await screen.findAllByText('remove');
    await user.click(removeButtons[1]);

    expect(await screen.findByText('Sara K. removed')).toBeInTheDocument();
    window.confirm = originalConfirm;
  });

  it('surfaces a toast instead of an unhandled rejection when removal fails', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const originalConfirm = window.confirm;
    window.confirm = () => true;
    mockApi({
      'GET /api/v1/orgs/acme/members': MEMBERS,
      'DELETE /api/v1/orgs/acme/members/u2': () => new Response(JSON.stringify({ detail: 'cannot remove the last owner' }), { status: 409 }),
    });

    renderPage(qc);

    const removeButtons = await screen.findAllByText('remove');
    await user.click(removeButtons[1]);

    const toast = await screen.findByRole('status');
    expect(toast).toHaveTextContent('cannot remove the last owner');
    window.confirm = originalConfirm;
  });

  it('shows the friendly access-denied page on a 403 (the members-spec requirement)', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mockApi({
      'GET /api/v1/orgs/acme/members': () => new Response(JSON.stringify({ detail: 'forbidden' }), { status: 403 }),
    });

    renderPage(qc);

    expect(await screen.findByText('You do not have access')).toBeInTheDocument();
  });
});
