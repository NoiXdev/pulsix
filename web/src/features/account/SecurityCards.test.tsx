import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ToastProvider } from '../../ui/toast.tsx';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { LockStateContext } from '../lock/lockState.ts';
import { mockApi } from '../../test/mockApi.ts';
import { AccountSecurityPage } from './AccountSecurityPage.tsx';

const ME = {
  user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
  mfa_complete: true,
  mfa_enrolment_required: false,
  totp_confirmed: true,
  instance_admin: false,
  must_change_password: false,
  recovery_codes_remaining: 8,
  lock_pin_set: true,
  lock_pin_legacy: false,
  session_expires_at: '2026-11-18T14:30:00Z',
};
const PASSKEYS = {
  passkeys: [
    { id: 'pk1', name: 'MacBook Pro', created_at: '2026-01-01T00:00:00Z' },
    { id: 'pk2', name: 'YubiKey', created_at: '2026-02-01T00:00:00Z' },
  ],
};

function renderPage(props: { onEnrolled?: () => void } = {}, locale?: 'de', setLocked = () => {}) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const page = (
    <LockStateContext.Provider value={{ locked: false, setLocked }}>
      <ToastProvider>
        <AccountSecurityPage {...props} />
      </ToastProvider>
    </LockStateContext.Provider>
  );
  return render(<QueryClientProvider client={qc}>{locale ? <I18nProvider locale={locale}>{page}</I18nProvider> : page}</QueryClientProvider>);
}

describe('Security page cards', () => {
  it('shows a Sign-in card with its rows and the passkeys under them, and a Session card', async () => {
    mockApi({ 'GET /api/v1/auth/me': ME, 'GET /api/v1/auth/passkeys': PASSKEYS });
    renderPage();

    const signIn = await screen.findByRole('region', { name: 'Sign-in' });
    expect(await within(signIn).findByText('Second factor active')).toBeInTheDocument();
    for (const label of ['Password', 'Authenticator app', 'Recovery codes', 'Passkeys']) {
      expect(within(signIn).getByText(label)).toBeInTheDocument();
    }
    expect(within(signIn).getByTestId('recovery-codes-remaining')).toHaveTextContent('8');
    expect(within(signIn).getByText('2 registered')).toBeInTheDocument();
    expect(within(signIn).getByText('MacBook Pro')).toBeInTheDocument();
    expect(within(signIn).getByText('YubiKey')).toBeInTheDocument();
    expect(within(signIn).getByRole('button', { name: 'Set up again…' })).toBeInTheDocument();
    // One primary button at most per card: the sign-in card has none.
    expect(signIn.querySelectorAll('button.bg-primary, button[data-variant="primary"]')).toHaveLength(0);

    const session = screen.getByRole('region', { name: 'Session' });
    expect(within(session).getByText('Lock PIN')).toBeInTheDocument();
    expect(within(session).getByText('This session')).toBeInTheDocument();
    expect(within(session).getByText(/^Expires at /)).toBeInTheDocument();
    expect(within(session).getByRole('button', { name: 'Change PIN…' })).toBeInTheDocument();
    expect(within(session).getByRole('button', { name: 'Lock now' })).toBeInTheDocument();
    expect(within(session).queryByText('No PIN')).not.toBeInTheDocument();
  });

  it('marks a missing PIN with a No PIN badge and offers no Lock now', async () => {
    mockApi({ 'GET /api/v1/auth/me': { ...ME, lock_pin_set: false }, 'GET /api/v1/auth/passkeys': PASSKEYS });
    renderPage();

    const session = await screen.findByRole('region', { name: 'Session' });
    expect(within(session).getByText('No PIN')).toBeInTheDocument();
    expect(within(session).getByRole('button', { name: 'Set PIN…' })).toBeInTheDocument();
    expect(within(session).queryByRole('button', { name: 'Lock now' })).not.toBeInTheDocument();
    expect(within(session).queryByRole('button', { name: 'Remove PIN' })).not.toBeInTheDocument();
  });

  it('flags a legacy PIN and asks for a new 4-digit one', async () => {
    mockApi({ 'GET /api/v1/auth/me': { ...ME, lock_pin_legacy: true }, 'GET /api/v1/auth/passkeys': PASSKEYS });
    renderPage();

    const session = await screen.findByRole('region', { name: 'Session' });
    expect(await within(session).findByText('Older PIN')).toBeInTheDocument();
    expect(within(session).getByText('Set a new 4-digit PIN.')).toBeInTheDocument();
  });

  it('locks the session with Lock now', async () => {
    const user = userEvent.setup();
    const lock = vi.fn(() => ({ ok: true }));
    const setLocked = vi.fn();
    mockApi({ 'GET /api/v1/auth/me': ME, 'GET /api/v1/auth/passkeys': PASSKEYS, 'POST /api/v1/auth/lock': lock });
    renderPage({}, undefined, setLocked);

    await user.click(await screen.findByRole('button', { name: 'Lock now' }));

    await waitFor(() => expect(lock).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(setLocked).toHaveBeenCalledWith(true));
  });

  it('opens each bigger flow in a dialog', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me': ME,
      'GET /api/v1/auth/passkeys': PASSKEYS,
      'POST /api/v1/auth/recovery-codes': { recovery_codes: ['aaaaa-bbbbb', 'ccccc-ddddd'] },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'Add passkey…' }));
    const passkey = await screen.findByRole('dialog', { name: 'Add a passkey' });
    expect(within(passkey).getByLabelText('Passkey name')).toBeInTheDocument();
    await user.click(within(passkey).getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.queryByLabelText('Passkey name')).not.toBeInTheDocument());

    await user.click(screen.getByRole('button', { name: 'Regenerate…' }));
    const codes = await screen.findByRole('dialog', { name: 'Recovery codes' });
    expect(within(codes).getByText('aaaaa-bbbbb')).toBeInTheDocument();
    await user.click(within(codes).getByRole('button', { name: 'I have saved these' }));
    await waitFor(() => expect(screen.queryByText('aaaaa-bbbbb')).not.toBeInTheDocument());

    await user.click(screen.getByRole('button', { name: 'Change PIN…' }));
    const pin = await screen.findByRole('dialog', { name: 'Change the lock PIN' });
    expect(within(pin).getByLabelText('PIN')).toBeInTheDocument();
  });

  it('speaks German', async () => {
    mockApi({ 'GET /api/v1/auth/me': { ...ME, lock_pin_legacy: true }, 'GET /api/v1/auth/passkeys': PASSKEYS });
    renderPage({}, 'de');

    const signIn = await screen.findByRole('region', { name: 'Anmeldung' });
    expect(await within(signIn).findByText('Zweiter Faktor aktiv')).toBeInTheDocument();
    expect(within(signIn).getByRole('button', { name: 'Passwort ändern…' })).toBeInTheDocument();
    const session = screen.getByRole('region', { name: 'Sitzung' });
    expect(await within(session).findByText('Ältere PIN')).toBeInTheDocument();
    expect(within(session).getByText('Lege eine neue 4-stellige PIN fest.')).toBeInTheDocument();
    expect(within(session).getByRole('button', { name: 'Jetzt sperren' })).toBeInTheDocument();
  });
});
