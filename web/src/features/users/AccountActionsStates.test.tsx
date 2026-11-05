import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { accountDetail } from './testFixtures.ts';
import { ACTIONS, renderAccountActions as render } from './renderAccountActions.tsx';

afterEach(() => vi.restoreAllMocks());

describe('AccountActions by account state', () => {
  it('renders no actions on the admin\'s own account and points to Account security', async () => {
    render(accountDetail({ id: 'u1' }), {}, 'u1');

    const link = await screen.findByRole('link', { name: 'Open Account security' });
    expect(link).toHaveAttribute('href', '/account/security');
    for (const label of ACTIONS) {
      expect(screen.queryByRole('button', { name: label })).not.toBeInTheDocument();
    }
  });

  it('offers only Deactivate for an account whose invitation is pending', async () => {
    render(accountDetail({ state: 'invite_pending', invite_pending: true }));

    expect(await screen.findByRole('button', { name: 'Deactivate account' })).toBeInTheDocument();
    for (const label of ACTIONS.slice(0, 4)) {
      expect(screen.queryByRole('button', { name: label })).not.toBeInTheDocument();
    }
    expect(screen.queryByRole('button', { name: 'Reactivate account' })).not.toBeInTheDocument();
  });

  it('offers only Reactivate for a deactivated account whose invitation is pending', async () => {
    render(accountDetail({ state: 'deactivated', invite_pending: true, disabled_at: '2026-10-01T00:00:00Z' }));

    expect(await screen.findByRole('button', { name: 'Reactivate account' })).toBeInTheDocument();
    for (const label of ACTIONS) {
      expect(screen.queryByRole('button', { name: label })).not.toBeInTheDocument();
    }
  });

  it('says the invitation, not the sign-in, is what Deactivate and Reactivate change for an invitee', async () => {
    const user = userEvent.setup();
    render(accountDetail({ state: 'invite_pending', invite_pending: true }));
    expect(await screen.findByText('The invitation can no longer be accepted.')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Deactivate account' }));
    const dialog = await screen.findByRole('dialog', { name: 'Deactivate account' });
    expect(dialog).toHaveTextContent('The invitation can no longer be accepted.');
    expect(dialog).not.toHaveTextContent(/sign in/);
  });

  it('says the invitation can be accepted again when an invitee is reactivated', async () => {
    const user = userEvent.setup();
    render(accountDetail({ state: 'deactivated', invite_pending: true, disabled_at: '2026-10-01T00:00:00Z' }));
    expect(await screen.findByText('The invitation can be accepted again.')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Reactivate account' }));
    const dialog = await screen.findByRole('dialog', { name: 'Reactivate account' });
    expect(dialog).toHaveTextContent('The invitation can be accepted again.');
    expect(dialog).not.toHaveTextContent(/sign in/);
  });

  it('keeps the sign-in wording for an account that has signed in', async () => {
    const user = userEvent.setup();
    render(accountDetail());
    await user.click(await screen.findByRole('button', { name: 'Deactivate account' }));
    const dialog = await screen.findByRole('dialog', { name: 'Deactivate account' });
    expect(dialog).toHaveTextContent('The person can no longer sign in.');
  });

  it('offers Reactivate instead of Deactivate for a deactivated account', async () => {
    const user = userEvent.setup();
    let reactivated = false;
    render(accountDetail({ state: 'deactivated', disabled_at: '2026-10-01T00:00:00Z' }), {
      'POST /api/v1/admin/users/u2/reactivate': () => {
        reactivated = true;
        return new Response(null, { status: 204 });
      },
    });

    await user.click(await screen.findByRole('button', { name: 'Reactivate account' }));
    expect(screen.queryByRole('button', { name: 'Deactivate account' })).not.toBeInTheDocument();
    const dialog = await screen.findByRole('dialog', { name: 'Reactivate account' });
    await user.click(within(dialog).getByRole('button', { name: 'Reactivate account' }));

    expect(await screen.findByText('Account reactivated.')).toBeInTheDocument();
    expect(reactivated).toBe(true);
  });
});
