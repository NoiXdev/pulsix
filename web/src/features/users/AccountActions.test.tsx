import { describe, expect, it } from 'vitest';
import { act, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/react-query';
import { mockApi, type MockRoute } from '../../test/mockApi.ts';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import type { AdminUserDetail } from '../../api/types.ts';
import { accountDetail } from './testFixtures.ts';
import { AccountActions } from './AccountActions.tsx';

const SECRET = 'Tmp-pass-9f3k2L-x7Q';

const stepUpRequired = () =>
  new Response(JSON.stringify({ title: 'Forbidden', status: 403, detail: 'step_up_required' }), { status: 403 });

function render(user: AdminUserDetail, routes: Record<string, MockRoute> = {}, selfId = 'u1', qc?: QueryClient) {
  mockApi({ 'GET /api/v1/auth/password/reset-available': { available: true }, ...routes });
  return renderRoutes(
    [{ path: '/settings/users/$userId', component: () => <AccountActions user={user} selfId={selfId} /> }],
    `/o/acme/settings/users/${user.id}`,
    qc,
  );
}

const ACTIONS = [
  'Send password reset link',
  'Set temporary password',
  'Reset second factor',
  'End all sessions',
  'Deactivate account',
];

describe('AccountActions', () => {
  it('asks for confirmation before every action and calls nothing until confirmed', async () => {
    const user = userEvent.setup();
    const calls: string[] = [];
    const record = (name: string) => () => {
      calls.push(name);
      return { notice: 'sent' };
    };
    render(accountDetail(), {
      'POST /api/v1/admin/users/u2/password-reset-link': record('link'),
      'POST /api/v1/admin/users/u2/temporary-password': record('tmp'),
      'POST /api/v1/admin/users/u2/second-factor/reset': record('factor'),
      'POST /api/v1/admin/users/u2/sessions/revoke': record('sessions'),
      'POST /api/v1/admin/users/u2/deactivate': record('deactivate'),
    });

    for (const label of ACTIONS) {
      const button = await screen.findByRole('button', { name: label });
      await waitFor(() => expect(button).toBeEnabled());
      await user.click(button);
      const dialog = await screen.findByRole('dialog', { name: label });
      await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
      await waitFor(() => expect(screen.queryByRole('dialog', { name: label })).not.toBeInTheDocument());
    }
    expect(calls).toEqual([]);
  });

  it('warns in the reset-factor dialog that anyone with the password can enrol, and advises a temporary password', async () => {
    const user = userEvent.setup();
    render(accountDetail());

    await user.click(await screen.findByRole('button', { name: 'Reset second factor' }));
    const dialog = await screen.findByRole('dialog', { name: 'Reset second factor' });
    expect(dialog).toHaveTextContent(/anyone with the password can set up a second factor/);
    expect(dialog).toHaveTextContent(/set a temporary password as well/);
  });

  it('shows the temporary password once with a copy button, and keeps it out of the DOM and every cache after closing', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      accountDetail(),
      {
        'POST /api/v1/admin/users/u2/temporary-password': {
          password: SECRET,
          expires_at: '2026-11-05T10:00:00Z',
          notice: 'sent',
        },
      },
      'u1',
      qc,
    );

    await user.click(await screen.findByRole('button', { name: 'Set temporary password' }));
    const confirmDialog = await screen.findByRole('dialog', { name: 'Set temporary password' });
    await user.click(within(confirmDialog).getByRole('button', { name: 'Set temporary password' }));

    const reveal = await screen.findByRole('dialog', { name: 'Temporary password' });
    expect(within(reveal).getByLabelText('Temporary password')).toHaveTextContent(SECRET);
    expect(reveal).toHaveTextContent(/separate channel/);
    // It names the account it belongs to, so it cannot be mistaken for
    // another account's.
    expect(reveal).toHaveTextContent('sara@acme.io');
    await user.click(within(reveal).getByRole('button', { name: 'Copy password' }));
    expect(await navigator.clipboard.readText()).toBe(SECRET);

    await user.click(within(reveal).getByRole('button', { name: 'Done' }));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Temporary password' })).not.toBeInTheDocument());
    expect(document.body.innerHTML).not.toContain(SECRET);
    const cached = [
      ...qc.getQueryCache().findAll().map((q) => q.state),
      ...qc.getMutationCache().getAll().map((m) => m.state),
    ];
    expect(JSON.stringify(cached)).not.toContain(SECRET);
  });

  it('keeps the dialog open on Escape while the temporary password is being set, with a busy note, then reveals it', async () => {
    const user = userEvent.setup();
    let answer: (body: unknown) => void = () => {};
    render(accountDetail(), {
      'POST /api/v1/admin/users/u2/temporary-password': () =>
        new Promise((resolve) => {
          answer = resolve;
        }),
    });

    await user.click(await screen.findByRole('button', { name: 'Set temporary password' }));
    const confirmDialog = await screen.findByRole('dialog', { name: 'Set temporary password' });
    await user.click(within(confirmDialog).getByRole('button', { name: 'Set temporary password' }));

    expect(await within(confirmDialog).findByText(/This can take a moment while the notice e-mail is sent/)).toBeInTheDocument();
    expect(within(confirmDialog).getByRole('button', { name: 'Cancel' })).toBeDisabled();
    await user.keyboard('{Escape}');
    expect(confirmDialog).toHaveAttribute('open');

    answer({ password: SECRET, expires_at: '2026-11-05T10:00:00Z', notice: 'sent' });
    const reveal = await screen.findByRole('dialog', { name: 'Temporary password' });
    expect(within(reveal).getByLabelText('Temporary password')).toHaveTextContent(SECRET);
  });

  // A real browser may close a <dialog> even while its cancel is
  // prevented (close watchers: a repeated Escape without user activation
  // fires a cancel that cannot be prevented). The answer must survive that.
  it('reveals the temporary password even when the confirm dialog was closed while the request ran', async () => {
    const user = userEvent.setup();
    let answer: (body: unknown) => void = () => {};
    render(accountDetail(), {
      'POST /api/v1/admin/users/u2/temporary-password': () =>
        new Promise((resolve) => {
          answer = resolve;
        }),
    });

    await user.click(await screen.findByRole('button', { name: 'Set temporary password' }));
    const confirmDialog = await screen.findByRole('dialog', { name: 'Set temporary password' });
    await user.click(within(confirmDialog).getByRole('button', { name: 'Set temporary password' }));
    await within(confirmDialog).findByText(/This can take a moment/);

    act(() => (confirmDialog as HTMLDialogElement).close());
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Set temporary password' })).not.toBeInTheDocument());
    // Nothing else can start while the request runs.
    expect(screen.getByRole('button', { name: 'Set temporary password' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'End all sessions' })).toBeDisabled();

    answer({ password: SECRET, expires_at: '2026-11-05T10:00:00Z', notice: 'sent' });
    const reveal = await screen.findByRole('dialog', { name: 'Temporary password' });
    expect(within(reveal).getByLabelText('Temporary password')).toHaveTextContent(SECRET);
    expect(screen.getByRole('button', { name: 'End all sessions' })).toBeEnabled();
  });

  it('reports a refusal as a toast when the confirm dialog was closed while the request ran', async () => {
    const user = userEvent.setup();
    let answer: (res: Response) => void = () => {};
    render(accountDetail({ instance_admin: true }), {
      'POST /api/v1/admin/users/u2/deactivate': () =>
        new Promise((resolve) => {
          answer = resolve;
        }),
    });

    await user.click(await screen.findByRole('button', { name: 'Deactivate account' }));
    const dialog = await screen.findByRole('dialog', { name: 'Deactivate account' });
    await user.click(within(dialog).getByRole('button', { name: 'Deactivate account' }));
    await within(dialog).findByRole('button', { name: 'Working…' });

    act(() => {
      dialog.dispatchEvent(new Event('cancel', { cancelable: false }));
      (dialog as HTMLDialogElement).close();
    });
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Deactivate account' })).not.toBeInTheDocument());

    answer(new Response(JSON.stringify({ detail: 'last_instance_admin' }), { status: 409 }));
    const toast = await screen.findByRole('status');
    expect(toast).toHaveTextContent('The last active instance administrator cannot be deactivated.');
    expect(toast).toHaveAttribute('data-tone', 'destructive');
  });

  it('says in the reveal when the notice mail failed', async () => {
    const user = userEvent.setup();
    render(accountDetail(), {
      'POST /api/v1/admin/users/u2/temporary-password': { password: SECRET, expires_at: '2026-11-05T10:00:00Z', notice: 'failed' },
    });

    await user.click(await screen.findByRole('button', { name: 'Set temporary password' }));
    const confirmDialog = await screen.findByRole('dialog', { name: 'Set temporary password' });
    await user.click(within(confirmDialog).getByRole('button', { name: 'Set temporary password' }));

    const reveal = await screen.findByRole('dialog', { name: 'Temporary password' });
    const line = within(reveal).getByText('The notice e-mail to the person could not be sent.');
    expect(line).toHaveAttribute('data-tone', 'warn');
  });

  it('keeps "Send password reset link" disabled with the reason until mail is known to be available', async () => {
    render(accountDetail(), {
      'GET /api/v1/auth/password/reset-available': () => new Promise(() => {}),
    });

    expect(await screen.findByRole('button', { name: 'Send password reset link' })).toBeDisabled();
    expect(screen.getByText('Checking whether outgoing mail is configured…')).toBeInTheDocument();
  });

  it('keeps "Send password reset link" disabled with the reason when the mail check failed', async () => {
    render(accountDetail(), {
      'GET /api/v1/auth/password/reset-available': () => new Response(JSON.stringify({ detail: 'boom' }), { status: 500 }),
    });

    expect(await screen.findByText('Could not check whether outgoing mail is configured, so no link can be sent.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Send password reset link' })).toBeDisabled();
  });

  it('disables "Send password reset link" with the reason when no mail is configured', async () => {
    render(accountDetail(), { 'GET /api/v1/auth/password/reset-available': { available: false } });

    expect(await screen.findByText('Outgoing mail is not configured on this instance, so no link can be sent.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Send password reset link' })).toBeDisabled();
  });

  it('shows a warning toast when the notice mail failed', async () => {
    const user = userEvent.setup();
    render(accountDetail(), { 'POST /api/v1/admin/users/u2/second-factor/reset': { notice: 'failed' } });

    await user.click(await screen.findByRole('button', { name: 'Reset second factor' }));
    const dialog = await screen.findByRole('dialog', { name: 'Reset second factor' });
    await user.click(within(dialog).getByRole('button', { name: 'Reset second factor' }));

    const toast = await screen.findByRole('status');
    expect(toast).toHaveTextContent('Second factor reset. The notice e-mail to the person could not be sent.');
    expect(toast).toHaveAttribute('data-tone', 'warn');
  });

  it('mentions neutrally that no mail went to an account that is not active', async () => {
    const user = userEvent.setup();
    render(accountDetail({ state: 'deactivated' }), { 'POST /api/v1/admin/users/u2/second-factor/reset': { notice: 'skipped' } });

    await user.click(await screen.findByRole('button', { name: 'Reset second factor' }));
    const dialog = await screen.findByRole('dialog', { name: 'Reset second factor' });
    await user.click(within(dialog).getByRole('button', { name: 'Reset second factor' }));

    const toast = await screen.findByRole('status');
    expect(toast).toHaveTextContent('No notice was e-mailed because the account is not active.');
    expect(toast).toHaveAttribute('data-tone', 'default');
  });

  it('asks for the second factor on step_up_required and retries the action after the proof', async () => {
    const user = userEvent.setup();
    let stale = true;
    let attempts = 0;
    render(accountDetail(), {
      'POST /api/v1/auth/totp/verify': () => {
        stale = false;
        return { ok: true };
      },
      'POST /api/v1/admin/users/u2/sessions/revoke': () => {
        attempts += 1;
        return stale ? stepUpRequired() : {};
      },
    });

    await user.click(await screen.findByRole('button', { name: 'End all sessions' }));
    const dialog = await screen.findByRole('dialog', { name: 'End all sessions' });
    await user.click(within(dialog).getByRole('button', { name: 'End all sessions' }));

    const stepUp = await screen.findByRole('dialog', { name: 'Confirm it is you' });
    await user.type(within(stepUp).getByLabelText('Authenticator or recovery code'), '123456');
    await user.click(within(stepUp).getByRole('button', { name: 'Confirm' }));

    expect(await screen.findByText('All sessions ended.')).toBeInTheDocument();
    expect(attempts).toBe(2);
  });

  it('shows the reason inside the dialog when the server refuses', async () => {
    const user = userEvent.setup();
    render(accountDetail({ instance_admin: true }), {
      'POST /api/v1/admin/users/u2/deactivate': () =>
        new Response(JSON.stringify({ detail: 'last_instance_admin' }), {
          status: 409,
        }),
    });

    await user.click(await screen.findByRole('button', { name: 'Deactivate account' }));
    const dialog = await screen.findByRole('dialog', { name: 'Deactivate account' });
    await user.click(within(dialog).getByRole('button', { name: 'Deactivate account' }));

    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'The last active instance administrator cannot be deactivated.',
    );
  });

  it('renders no actions on the admin\'s own account and points to Account security', async () => {
    render(accountDetail({ id: 'u1' }), {}, 'u1');

    const link = await screen.findByRole('link', { name: 'Open Account security' });
    expect(link).toHaveAttribute('href', '/account/security');
    for (const label of ACTIONS) {
      expect(screen.queryByRole('button', { name: label })).not.toBeInTheDocument();
    }
  });

  it('offers only Deactivate for an account whose invitation is pending', async () => {
    render(accountDetail({ state: 'invite_pending' }));

    expect(await screen.findByRole('button', { name: 'Deactivate account' })).toBeInTheDocument();
    for (const label of ACTIONS.slice(0, 4)) {
      expect(screen.queryByRole('button', { name: label })).not.toBeInTheDocument();
    }
    expect(screen.queryByRole('button', { name: 'Reactivate account' })).not.toBeInTheDocument();
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
