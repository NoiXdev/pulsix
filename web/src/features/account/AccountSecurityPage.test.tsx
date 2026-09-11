import { beforeEach, describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { fetchMock } from '../../test/fetchMock.ts';
import { AccountSecurityPage } from './AccountSecurityPage.tsx';

function renderPage(qc: QueryClient) {
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <AccountSecurityPage />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

const NO_PASSKEYS = { passkeys: [] };

describe('AccountSecurityPage status pill (Important 4)', () => {
  let qc: QueryClient;
  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  });

  it('shows "second factor required" when totp is unconfirmed and there is no passkey', async () => {
    mockApi({
      'GET /api/v1/auth/me': {
        user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
        mfa_complete: true,
        totp_confirmed: false,
        instance_admin: true,
      },
      'GET /api/v1/auth/passkeys': NO_PASSKEYS,
    });

    renderPage(qc);

    expect(await screen.findByText('second factor required')).toBeInTheDocument();
    expect(screen.queryByText('second factor active')).not.toBeInTheDocument();
  });

  it('does not show "active" merely because mfa_complete is true with no factor enrolled (the backend bug this finding is about)', async () => {
    mockApi({
      'GET /api/v1/auth/me': {
        user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
        // A backend that treats "no factor at all" as MFA-complete —
        // exactly the trap Important 4 describes.
        mfa_complete: true,
        totp_confirmed: false,
        instance_admin: true,
      },
      'GET /api/v1/auth/passkeys': NO_PASSKEYS,
    });

    renderPage(qc);

    await screen.findByText('second factor required');
  });

  it('shows "second factor active" when totp_confirmed is true', async () => {
    mockApi({
      'GET /api/v1/auth/me': {
        user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
        mfa_complete: true,
        totp_confirmed: true,
        instance_admin: true,
      },
      'GET /api/v1/auth/passkeys': NO_PASSKEYS,
    });

    renderPage(qc);

    expect(await screen.findByText('second factor active')).toBeInTheDocument();
  });

  it('shows "second factor active" when a passkey exists even if totp is unconfirmed', async () => {
    mockApi({
      'GET /api/v1/auth/me': {
        user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
        mfa_complete: true,
        totp_confirmed: false,
        instance_admin: true,
      },
      'GET /api/v1/auth/passkeys': {
        passkeys: [{ id: 'pk1', name: 'MacBook Pro', created_at: '2026-01-01T00:00:00Z' }],
      },
    });

    renderPage(qc);

    expect(await screen.findByText('second factor active')).toBeInTheDocument();
  });
});

describe('AccountSecurityPage TOTP secret handling (Critical fix)', () => {
  let qc: QueryClient;
  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  });

  it('sends no "code" field at all for a first-time enrolment, not an empty one — an empty string still 422s against the backend\'s ^[0-9]{6}$ pattern', async () => {
    const user = userEvent.setup();
    const enrol = mockApi({
      'GET /api/v1/auth/me': {
        user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
        mfa_complete: false,
        totp_confirmed: false,
        instance_admin: true,
      },
      'GET /api/v1/auth/passkeys': NO_PASSKEYS,
      'POST /api/v1/auth/totp/enrol': { secret: 'JBSWY3DPEHPK3PXP', otpauth_url: 'otpauth://totp/pulsix?secret=JBSWY3DPEHPK3PXP' },
    });

    renderPage(qc);
    await user.click(await screen.findByRole('button', { name: 'Enrol authenticator app' }));
    await screen.findByText('JBSWY3DPEHPK3PXP');

    const call = enrol.mock.calls.find(([req]) => (req instanceof Request ? req.url : req.toString()).includes('/auth/totp/enrol'));
    const sentBody = call?.[0] instanceof Request ? await call[0].clone().text() : undefined;
    expect(sentBody).toBe('{}');
  });

  it('leaves no mutation cache entry carrying the TOTP secret or recovery codes once the recovery codes are dismissed', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me': {
        user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
        mfa_complete: false,
        totp_confirmed: false,
        instance_admin: true,
      },
      'GET /api/v1/auth/passkeys': NO_PASSKEYS,
      'POST /api/v1/auth/totp/enrol': {
        secret: 'JBSWY3DPEHPK3PXP',
        otpauth_url: 'otpauth://totp/pulsix?secret=JBSWY3DPEHPK3PXP',
      },
      'POST /api/v1/auth/totp/confirm': {
        recovery_codes: ['aaaa-bbbb', 'cccc-dddd'],
      },
    });

    renderPage(qc);

    await user.click(await screen.findByRole('button', { name: 'Enrol authenticator app' }));
    expect(await screen.findByText('JBSWY3DPEHPK3PXP')).toBeInTheDocument();

    await user.type(screen.getByPlaceholderText('482913'), '111111');
    await user.click(screen.getByRole('button', { name: 'Confirm' }));

    expect(await screen.findByText('aaaa-bbbb')).toBeInTheDocument();

    // Before dismissal: the secret and the recovery codes are reachable
    // through the mutation cache — this is the Critical vulnerability
    // itself, asserted here so the test would fail if the dismiss button
    // (or gcTime: 0) were ever removed.
    const mutationsBeforeDismiss = qc.getMutationCache().getAll();
    const cachedDataBeforeDismiss = mutationsBeforeDismiss.map((m) => JSON.stringify(m.state.data));
    expect(cachedDataBeforeDismiss.some((d) => d.includes('aaaa-bbbb'))).toBe(true);

    await user.click(await screen.findByRole('button', { name: 'I have saved these' }));

    await waitFor(() => {
      const mutations = qc.getMutationCache().getAll();
      const cachedData = mutations.map((m) => JSON.stringify(m.state.data));
      expect(cachedData.some((d) => d.includes('JBSWY3DPEHPK3PXP'))).toBe(false);
      expect(cachedData.some((d) => d.includes('aaaa-bbbb'))).toBe(false);
    });

    expect(screen.queryByText('aaaa-bbbb')).not.toBeInTheDocument();
  });
});

describe('AccountSecurityPage error handling', () => {
  it('surfaces a toast instead of an unhandled rejection when enrolment fails', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const user = userEvent.setup();
    // fetchMock.mockImplementation directly (not mockApi, which always
    // 200s a matched route) so the enrol call itself 400s and exercises
    // the catch path.
    fetchMock.mockImplementation(async (input: RequestInfo | URL) => {
      const url = input instanceof Request ? input.url : input.toString();
      if (url.includes('/auth/totp/enrol')) {
        return new Response(JSON.stringify({ detail: 'a fresh code is required to re-enrol' }), { status: 400 });
      }
      if (url.includes('/auth/me')) {
        return new Response(
          JSON.stringify({
            user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
            mfa_complete: true,
            totp_confirmed: true,
            instance_admin: true,
          }),
          { status: 200 },
        );
      }
      if (url.includes('/auth/passkeys')) {
        return new Response(JSON.stringify(NO_PASSKEYS), { status: 200 });
      }
      return new Response('{}', { status: 404 });
    });

    render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <AccountSecurityPage />
        </ToastProvider>
      </QueryClientProvider>,
    );

    await user.click(await screen.findByRole('button', { name: 'Enrol authenticator app' }));

    const toast = await screen.findByRole('status');
    expect(within(toast).getByText('a fresh code is required to re-enrol')).toBeInTheDocument();
  });
});
