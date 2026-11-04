import { describe, expect, it } from 'vitest';
import { screen, within } from '@testing-library/react';
import { mockApi } from '../../test/mockApi.ts';
import { ADMIN, renderRoutes } from '../../test/renderRoutes.tsx';
import { AccountPage } from './AccountPage.tsx';
import { accountDetail } from './testFixtures.ts';

function render() {
  return renderRoutes([{ path: '/settings/users/$userId', component: AccountPage }], '/o/acme/settings/users/u2');
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
    expect(within(orgs).getByText('owner')).toBeInTheDocument();

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
});
