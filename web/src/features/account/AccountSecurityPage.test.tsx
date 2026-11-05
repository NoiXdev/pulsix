import { beforeEach, describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { fetchMock } from '../../test/fetchMock.ts';
import { pickOption } from '../../test/pickOption.ts';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
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

  it('does not show "active" merely because mfa_complete is true with no factor enrolled', async () => {
    mockApi({
      'GET /api/v1/auth/me': {
        user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
        // mfa_complete describes the session, not the account: the pill
        // must read the account's factors, never this flag (Important 4).
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

describe('step-up (backlog 1.28)', () => {
  it('re-asks for the second factor when the server says the proof is stale, then completes the action', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let stale = true;
    let verified = 0;
    let enrolAttempts = 0;
    mockApi({
      'GET /api/v1/auth/me': { id: 'u1', email: 'a@b.c', display_name: 'A', instance_admin: false, mfa_complete: true, totp_confirmed: false },
      'GET /api/v1/auth/passkeys': { passkeys: [{ id: 'p1', name: 'yubikey', created_at: new Date().toISOString() }] },
      'POST /api/v1/auth/totp/verify': () => {
        verified += 1;
        stale = false;
        return { ok: true };
      },
      'POST /api/v1/auth/totp/enrol': () => {
        enrolAttempts += 1;
        return stale
          ? new Response(JSON.stringify({ title: 'Forbidden', status: 403, detail: 'step_up_required' }), { status: 403 })
          : { secret: 'JBSWY3DPEHPK3PXP', otpauth_url: 'otpauth://totp/Pulsix:a@b.c?secret=JBSWY3DPEHPK3PXP' };
      },
    });
    renderPage(qc);

    await user.click(await screen.findByRole('button', { name: 'Enrol authenticator app' }));

    // No error toast — a dialog asking for the factor.
    expect(await screen.findByText('Confirm it is you')).toBeInTheDocument();
    await user.type(screen.getByLabelText('Authenticator or recovery code'), '123456');
    await user.click(screen.getByRole('button', { name: 'Confirm' }));

    // The factor is re-proved once, and the action the user actually
    // wanted goes through without them repeating it.
    await waitFor(() => expect(verified).toBe(1));
    expect(await screen.findByText(/Scan this with your authenticator|JBSWY3DPEHPK3PXP/)).toBeInTheDocument();
    expect(enrolAttempts).toBe(2);
  });
});

describe('step-up refused again', () => {
  const ME = {
    user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
    mfa_complete: true,
    mfa_enrolment_required: false,
    totp_confirmed: true,
    instance_admin: false,
    must_change_password: false,
  };

  it('shows that confirming the identity did not work when the retry after the proof is refused again', async () => {
    const user = userEvent.setup();
    const stale = () =>
      new Response(JSON.stringify({ title: 'Forbidden', status: 403, detail: 'step_up_required' }), { status: 403 });
    mockApi({
      'GET /api/v1/auth/me': ME,
      'GET /api/v1/auth/passkeys': NO_PASSKEYS,
      'POST /api/v1/auth/totp/verify': { ok: true },
      'POST /api/v1/auth/password': stale,
    });
    renderPage(new QueryClient({ defaultOptions: { queries: { retry: false } } }));

    await user.type(await screen.findByLabelText('Current password'), 'my-old-long-password');
    await user.type(screen.getByLabelText('New password'), 'my-new-long-password');
    await user.type(screen.getByLabelText('Repeat the new password'), 'my-new-long-password');
    await user.click(screen.getByRole('button', { name: 'Change password' }));
    await user.type(await screen.findByLabelText('Authenticator or recovery code'), '123456');
    await user.click(screen.getByRole('button', { name: 'Confirm' }));

    expect(await screen.findByText('Confirming your identity did not work. Try again.')).toBeInTheDocument();
  });
});

// Backlog 1.34 and the count beside it. The set is shown once, at
// enrolment; after that nothing told the operator how many were left, and
// nothing let them get more. The two belong on screen together: the number
// is what makes the action look worth taking before it is urgent.
describe('recovery codes', () => {
  let qc: QueryClient;
  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  });

  it('shows how many codes are left', async () => {
    mockApi({
      'GET /api/v1/auth/me': {
        user: { id: 'u1', email: 'a@b.test', display_name: 'A' },
        mfa_complete: true,
        totp_confirmed: true,
        recovery_codes_remaining: 3,
      },
      'GET /api/v1/auth/passkeys': NO_PASSKEYS,
    });
    renderPage(qc);
    await waitFor(() => {
      expect(screen.getByTestId('recovery-codes-remaining')).toHaveTextContent('3');
    });
  });

  it('replaces the set and shows the new codes exactly once', async () => {
    mockApi({
      'GET /api/v1/auth/me': {
        user: { id: 'u1', email: 'a@b.test', display_name: 'A' },
        mfa_complete: true,
        totp_confirmed: true,
        recovery_codes_remaining: 1,
      },
      'GET /api/v1/auth/passkeys': NO_PASSKEYS,
      'POST /api/v1/auth/recovery-codes': { recovery_codes: ['aaaaa-bbbbb-ccccc', 'ddddd-eeeee-fffff'] },
    });
    renderPage(qc);

    await userEvent.click(await screen.findByRole('button', { name: /regenerate recovery codes/i }));

    // The codes themselves must appear — this response is the only time
    // they exist in readable form.
    await waitFor(() => {
      expect(screen.getByText('aaaaa-bbbbb-ccccc')).toBeInTheDocument();
    });
    expect(screen.getByText('ddddd-eeeee-fffff')).toBeInTheDocument();
  });
});

describe('the Language card', () => {
  const ME = {
    user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
    mfa_complete: true,
    totp_confirmed: true,
    instance_admin: false,
  };

  function renderWithI18n(onEnrolled?: () => void) {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <I18nProvider>
          <ToastProvider>
            <AccountSecurityPage onEnrolled={onEnrolled} />
          </ToastProvider>
        </I18nProvider>
      </QueryClientProvider>,
    );
  }

  it('offers the instance default, English and Deutsch, and switches the page on a choice (Review Focus 3)', async () => {
    const saved: unknown[] = [];
    let mine: 'en' | 'de' | null = null;
    mockApi({
      'GET /api/v1/auth/me': () => ({ ...ME, locale: mine, effective_locale: mine ?? 'en' }),
      'GET /api/v1/auth/passkeys': NO_PASSKEYS,
      'GET /api/v1/setup/status': { completed: true, version: 'dev', default_locale: 'en' },
      'PUT /api/v1/auth/me/locale': (req: { body: unknown }) => {
        saved.push(req.body);
        mine = (req.body as { locale: 'en' | 'de' | null }).locale;
        return { locale: mine, effective_locale: mine ?? 'en' };
      },
    });
    renderWithI18n();

    const field = await screen.findByLabelText('Language');
    await waitFor(() => expect(field).toHaveValue('Instance default (English)'));

    await pickOption(screen, 'Language', 'Deutsch');
    // The whole page follows, not just the card.
    expect(await screen.findByRole('heading', { name: 'Sprache' })).toBeInTheDocument();
    expect(saved).toEqual([{ locale: 'de' }]);
    expect(document.documentElement.lang).toBe('de');

    await pickOption(screen, 'Sprache', 'Standard der Instanz (English)');
    expect(await screen.findByRole('heading', { name: 'Language' })).toBeInTheDocument();
    expect(saved).toEqual([{ locale: 'de' }, { locale: null }]);
  });

  it('shows the API error when saving fails', async () => {
    mockApi({
      'GET /api/v1/auth/me': { ...ME, locale: null, effective_locale: 'en' },
      'GET /api/v1/auth/passkeys': NO_PASSKEYS,
      'GET /api/v1/setup/status': { completed: true, version: 'dev', default_locale: 'en' },
      'PUT /api/v1/auth/me/locale': () =>
        new Response(JSON.stringify({ detail: 'this needs a signed-in session' }), {
          status: 403,
          headers: { 'Content-Type': 'application/json' },
        }),
    });
    renderWithI18n();
    await screen.findByLabelText('Language');
    await pickOption(screen, 'Language', 'Deutsch');
    const toast = await screen.findByRole('status');
    expect(within(toast).getByText('this needs a signed-in session')).toBeInTheDocument();
  });

  it('is left out of second-factor enrolment, which has one job', async () => {
    mockApi({
      'GET /api/v1/auth/me': { ...ME, totp_confirmed: false, locale: null, effective_locale: 'en' },
      'GET /api/v1/auth/passkeys': NO_PASSKEYS,
      'GET /api/v1/setup/status': { completed: true, version: 'dev', default_locale: 'en' },
    });
    renderWithI18n(() => {});
    await screen.findByRole('button', { name: 'Enrol authenticator app' });
    expect(screen.queryByLabelText('Language')).not.toBeInTheDocument();
  });
});

describe('the Password card', () => {
  const ME = {
    user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
    mfa_complete: true,
    mfa_enrolment_required: false,
    totp_confirmed: true,
    instance_admin: false,
    must_change_password: false,
  };

  async function change(user: ReturnType<typeof userEvent.setup>) {
    await user.type(await screen.findByLabelText('Current password'), 'my-old-long-password');
    await user.type(screen.getByLabelText('New password'), 'my-new-long-password');
    await user.type(screen.getByLabelText('Repeat the new password'), 'my-new-long-password');
    await user.click(screen.getByRole('button', { name: 'Change password' }));
  }

  it('renders on Account security with the current and the new password', async () => {
    mockApi({ 'GET /api/v1/auth/me': ME, 'GET /api/v1/auth/passkeys': NO_PASSKEYS });
    renderPage(new QueryClient({ defaultOptions: { queries: { retry: false } } }));

    expect(await screen.findByRole('heading', { name: 'Password' })).toBeInTheDocument();
    expect(screen.getByLabelText('Current password')).toHaveAttribute('type', 'password');
    expect(screen.getByLabelText('New password')).toHaveAttribute('type', 'password');
    expect(screen.getByLabelText('Repeat the new password')).toHaveAttribute('type', 'password');
  });

  it('asks for the second factor on step_up_required, then changes the password and says the other sessions ended', async () => {
    const user = userEvent.setup();
    let stale = true;
    const posted: unknown[] = [];
    mockApi({
      'GET /api/v1/auth/me': ME,
      'GET /api/v1/auth/passkeys': NO_PASSKEYS,
      'POST /api/v1/auth/totp/verify': () => {
        stale = false;
        return {};
      },
      'POST /api/v1/auth/password': (req: { body: unknown }) => {
        if (stale) {
          return new Response(JSON.stringify({ title: 'Forbidden', status: 403, detail: 'step_up_required' }), { status: 403 });
        }
        posted.push(req.body);
        return new Response(null, { status: 204 });
      },
    });
    renderPage(new QueryClient({ defaultOptions: { queries: { retry: false } } }));

    await change(user);
    expect(await screen.findByText('Confirm it is you')).toBeInTheDocument();
    await user.type(screen.getByLabelText('Authenticator or recovery code'), '123456');
    await user.click(screen.getByRole('button', { name: 'Confirm' }));

    const toast = await screen.findByRole('status');
    expect(toast).toHaveTextContent(/Password changed/);
    expect(toast).toHaveTextContent(/other sessions/);
    expect(posted).toEqual([{ current_password: 'my-old-long-password', new_password: 'my-new-long-password' }]);
    // The form starts over: no password stays in a field.
    expect(screen.getByLabelText('Current password')).toHaveValue('');
    expect(screen.getByLabelText('New password')).toHaveValue('');
  });

  it('says so when the current password is wrong', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me': ME,
      'GET /api/v1/auth/passkeys': NO_PASSKEYS,
      'POST /api/v1/auth/password': () =>
        new Response(JSON.stringify({ title: 'Bad Request', status: 400, detail: 'invalid_current_password' }), { status: 400 }),
    });
    renderPage(new QueryClient({ defaultOptions: { queries: { retry: false } } }));

    await change(user);

    expect(await screen.findByRole('alert')).toHaveTextContent('The current password is not correct.');
  });

  it('is left out of second-factor enrolment, which has one job', async () => {
    mockApi({ 'GET /api/v1/auth/me': { ...ME, totp_confirmed: false }, 'GET /api/v1/auth/passkeys': NO_PASSKEYS });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <AccountSecurityPage onEnrolled={() => {}} />
        </ToastProvider>
      </QueryClientProvider>,
    );

    await screen.findByRole('button', { name: 'Enrol authenticator app' });
    expect(screen.queryByLabelText('Current password')).not.toBeInTheDocument();
  });
});
