import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi } from '../../test/mockApi.ts';
import { ADMIN, renderRoutes } from '../../test/renderRoutes.tsx';
import { AccountPage } from './AccountPage.tsx';
import { accountDetail } from './testFixtures.ts';

function render(initial = '/o/acme/settings/users/u2') {
  return renderRoutes([{ path: '/settings/users/$userId', component: AccountPage }], initial);
}

describe('AccountPage', () => {
  it('shows the account in the sidebar and its organizations, sign-in and actions sections', async () => {
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/auth/password/reset-available': { available: true },
      'GET /api/v1/admin/users/u2': {
        user: accountDetail({
          state: 'no_second_factor',
          last_sign_in_at: null,
          memberships: [
            { org_id: 'o1', slug: 'acme', name: 'Acme', role: 'viewer', actor_is_member: true },
            { org_id: 'o2', slug: 'globex', name: 'Globex', role: 'owner', actor_is_member: false },
          ],
          factors: { totp: false, passkeys: 0, recovery_codes_left: 0 },
        }),
      },
    });
    render();

    expect(await screen.findByRole('heading', { level: 1, name: 'Sara K.' })).toBeInTheDocument();
    expect(screen.getByText('sara@acme.io')).toBeInTheDocument();
    expect(screen.getByText('No second factor')).toBeInTheDocument();
    expect(screen.getByText('never')).toBeInTheDocument();

    const orgs = screen.getByRole('region', { name: 'Organizations' });
    expect(within(orgs).getByRole('link', { name: 'Open the member page in Acme' })).toHaveAttribute(
      'href',
      '/o/acme/settings/members/u2',
    );
    expect(within(orgs).getByText('Globex')).toBeInTheDocument();
    expect(within(orgs).queryByRole('link', { name: 'Open the member page in Globex' })).not.toBeInTheDocument();
    expect(within(orgs).getByText('Owner')).toBeInTheDocument();

    const signIn = screen.getByRole('region', { name: 'Sign-in' });
    expect(within(signIn).getByText('Not set up')).toBeInTheDocument();
    expect(within(signIn).getByText('3')).toBeInTheDocument();

    const actions = screen.getByRole('region', { name: 'Actions' });
    expect(await within(actions).findByRole('button', { name: 'Set temporary password' })).toBeInTheDocument();
  });

  it('shows the access-denied page to someone who is not an instance administrator', async () => {
    mockApi({
      'GET /api/v1/auth/me': { ...ADMIN, instance_admin: false },
      'GET /api/v1/admin/users/u2': () =>
        new Response(JSON.stringify({ detail: 'instance admin required' }), { status: 403 }),
    });
    render();
    expect(await screen.findByText('You do not have access')).toBeInTheDocument();
  });

  // The router keeps the page mounted when only the account in the address
  // changes; a temporary password must not be left on screen beside
  // another account.
  it('drops an open temporary-password reveal when the page moves to another account', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/auth/password/reset-available': { available: true },
      'GET /api/v1/admin/users/u2': { user: accountDetail() },
      'GET /api/v1/admin/users/u3': { user: accountDetail({ id: 'u3', email: 'tom@acme.io', display_name: 'Tom B.' }) },
      'POST /api/v1/admin/users/u2/temporary-password': {
        password: 'Tmp-pass-9f3k2L-x7Q',
        expires_at: '2026-11-05T10:00:00Z',
        notice: 'sent',
      },
    });
    const router = render('/o/acme/settings/users/u3');
    expect(await screen.findByRole('heading', { level: 1, name: 'Tom B.' })).toBeInTheDocument();
    await router.navigate({ to: '/o/$org/settings/users/$userId', params: { org: 'acme', userId: 'u2' } });
    expect(await screen.findByRole('heading', { level: 1, name: 'Sara K.' })).toBeInTheDocument();

    await user.click(await screen.findByRole('button', { name: 'Set temporary password' }));
    const confirm = await screen.findByRole('dialog', { name: 'Set temporary password' });
    await user.click(within(confirm).getByRole('button', { name: 'Set temporary password' }));
    expect(await screen.findByRole('dialog', { name: 'Temporary password' })).toHaveTextContent('sara@acme.io');

    await router.navigate({ to: '/o/$org/settings/users/$userId', params: { org: 'acme', userId: 'u3' } });
    expect(await screen.findByRole('heading', { level: 1, name: 'Tom B.' })).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Temporary password' })).not.toBeInTheDocument());
    expect(document.body.innerHTML).not.toContain('Tmp-pass-9f3k2L-x7Q');
  });
});
