import { describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest, type MockRoute } from '../../test/mockApi.ts';
import { ADMIN, renderRoutes } from '../../test/renderRoutes.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { accountDetail } from '../users/testFixtures.ts';
import { MemberPage } from './MemberPage.tsx';

const MEMBERS = {
  members: [
    { user_id: 'u1', display_name: 'Ada', email: 'admin@acme.io', role: 'owner', created_at: '2026-01-01T00:00:00Z' },
    { user_id: 'u2', display_name: 'Sara K.', email: 'sara@acme.io', role: 'viewer', created_at: '2026-02-01T00:00:00Z' },
  ],
};

const org = (role: string) => ({ org: { id: 'o1', name: 'Acme', slug: 'acme', role, created_at: '2026-01-01T00:00:00Z' } });

function render(opts: { role: string; instanceAdmin: boolean }, routes: Record<string, MockRoute> = {}) {
  const adminCalls: string[] = [];
  mockApi({
    'GET /api/v1/auth/me': { ...ADMIN, instance_admin: opts.instanceAdmin },
    'GET /api/v1/orgs/acme': org(opts.role),
    'GET /api/v1/orgs/acme/members': MEMBERS,
    'GET /api/v1/auth/password/reset-available': { available: true },
    'GET /api/v1/admin/users/u2': (req: MockRequest) => {
      adminCalls.push(req.path);
      return { user: accountDetail() };
    },
    ...routes,
  });
  const router = renderRoutes(
    [
      { path: '/settings/members/$userId', component: MemberPage },
      { path: '/settings/members', component: () => <div>members list</div> },
    ],
    '/o/acme/settings/members/u2',
  );
  return { router, adminCalls };
}

describe('MemberPage', () => {
  it('shows an organization admin the role controls and no account sections', async () => {
    const { adminCalls } = render({ role: 'admin', instanceAdmin: false });

    expect(await screen.findByRole('heading', { level: 1, name: 'Sara K.' })).toBeInTheDocument();
    expect(await screen.findByLabelText('Role in this organization')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Change role' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Remove from organization' })).toBeInTheDocument();
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByRole('region', { name: 'Sign-in' })).not.toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'Actions' })).not.toBeInTheDocument();
    expect(adminCalls).toEqual([]);
  });

  it('shows an instance admin the role controls and the account sections', async () => {
    render({ role: 'owner', instanceAdmin: true });

    expect(await screen.findByRole('region', { name: 'Sign-in' })).toBeInTheDocument();
    expect(screen.getByRole('region', { name: 'Actions' })).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: 'Set temporary password' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Change role' })).toBeInTheDocument();
  });

  it('shows a viewer the role without controls', async () => {
    render({ role: 'viewer', instanceAdmin: false });

    expect(await screen.findByText('Only owners and admins can change roles or remove members.')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Change role' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Remove from organization' })).not.toBeInTheDocument();
  });

  it('changes the role', async () => {
    const user = userEvent.setup();
    let changedTo: string | undefined;
    render(
      { role: 'admin', instanceAdmin: false },
      {
        'PATCH /api/v1/orgs/acme/members/u2': (req: MockRequest) => {
          changedTo = (req.body as { role: string }).role;
          return {};
        },
      },
    );

    await screen.findByLabelText('Role in this organization');
    await pickOption(screen, 'Role in this organization', 'operator');
    await user.click(screen.getByRole('button', { name: 'Change role' }));

    expect(await screen.findByText('Role updated to operator')).toBeInTheDocument();
    expect(changedTo).toBe('operator');
  });

  it('shows the server\'s reason when a role change is refused', async () => {
    const user = userEvent.setup();
    render(
      { role: 'admin', instanceAdmin: false },
      {
        'PATCH /api/v1/orgs/acme/members/u2': () =>
          new Response(JSON.stringify({ detail: 'organization needs at least one owner' }), { status: 409 }),
      },
    );

    await screen.findByLabelText('Role in this organization');
    await pickOption(screen, 'Role in this organization', 'operator');
    await user.click(screen.getByRole('button', { name: 'Change role' }));

    expect(await screen.findByText('organization needs at least one owner')).toBeInTheDocument();
  });

  it('removes the member after confirmation and returns to the list', async () => {
    const user = userEvent.setup();
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true);
    let removed = false;
    const { router } = render(
      { role: 'admin', instanceAdmin: false },
      {
        'DELETE /api/v1/orgs/acme/members/u2': () => {
          removed = true;
          return {};
        },
      },
    );

    await user.click(await screen.findByRole('button', { name: 'Remove from organization' }));

    expect(await screen.findByText('members list')).toBeInTheDocument();
    expect(removed).toBe(true);
    await waitFor(() => expect(router.state.location.pathname).toBe('/o/acme/settings/members'));
    confirmSpy.mockRestore();
  });

  it('says the member does not exist when the id is not in this organization', async () => {
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/members': MEMBERS,
    });
    renderRoutes([{ path: '/settings/members/$userId', component: MemberPage }], '/o/acme/settings/members/u7');
    expect(await screen.findByText('Not found')).toBeInTheDocument();
  });
});
