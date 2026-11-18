import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi } from '../../test/mockApi.ts';
import { fetchMock } from '../../test/fetchMock.ts';
import { StepUpDialog } from './StepUpDialog.tsx';
import { meQueryKey } from '../../api/hooks/useMe.ts';

vi.mock('../auth/webauthnCodec.ts', async (orig) => ({
  ...(await orig<typeof import('../auth/webauthnCodec.ts')>()),
  decodeRequestOptions: (o: unknown) => o,
  encodeAssertionCredential: () => ({ id: 'cred' }),
}));

describe('StepUpDialog passkey', () => {
  it('proves the passkey through the re-auth routes and never the login ones', async () => {
    Object.defineProperty(navigator, 'credentials', { value: { get: vi.fn().mockResolvedValue({ id: 'cred' }) }, configurable: true });
    Object.defineProperty(window, 'PublicKeyCredential', { value: class {}, configurable: true });
    const finish = vi.fn(() => ({}));
    mockApi({
      'POST /api/v1/auth/passkeys/reauth/begin': { ceremony_id: 'c1', options: { publicKey: { challenge: 'x' } } },
      'POST /api/v1/auth/passkeys/reauth/finish': finish,
    });
    const onProved = vi.fn();
    render(
      <QueryClientProvider client={new QueryClient()}>
        <StepUpDialog onClose={() => {}} onProved={onProved} />
      </QueryClientProvider>,
    );
    await userEvent.click(screen.getByRole('button', { name: 'Use a passkey' }));

    await waitFor(() => expect(onProved).toHaveBeenCalledTimes(1));
    expect(finish).toHaveBeenCalledWith(expect.objectContaining({ body: { ceremony_id: 'c1', response: { id: 'cred' } } }));
    const paths = fetchMock.mock.calls.map(([input]) => new URL((input as Request).url, 'http://localhost').pathname);
    expect(paths.some((p) => p.includes('/passkeys/login/'))).toBe(false);
    expect(paths).toContain('/api/v1/auth/passkeys/reauth/begin');
  });
});

describe('StepUpDialog with the second factor optional', () => {
  const ME = {
    user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
    mfa_complete: true,
    mfa_enrolment_required: false,
    totp_confirmed: false,
    instance_admin: false,
  };

  it('asks a factor-less account for its password and proves it through the password step-up', async () => {
    const reauth = vi.fn(() => ({}));
    mockApi({ 'POST /api/v1/auth/password/reauth': reauth });
    const onProved = vi.fn();
    const qc = new QueryClient();
    qc.setQueryData(meQueryKey, { ...ME, password_step_up: true });
    render(
      <QueryClientProvider client={qc}>
        <StepUpDialog onClose={() => {}} onProved={onProved} />
      </QueryClientProvider>,
    );

    await userEvent.type(await screen.findByLabelText('Password'), 'correct horse battery');
    expect(screen.queryByLabelText('Authenticator or recovery code')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Use a passkey' })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Confirm' }));

    await waitFor(() => expect(onProved).toHaveBeenCalledTimes(1));
    expect(reauth).toHaveBeenCalledWith(expect.objectContaining({ body: { password: 'correct horse battery' } }));
  });

  it('keeps asking an account with a factor for the factor', async () => {
    mockApi({});
    const qc = new QueryClient();
    qc.setQueryData(meQueryKey, { ...ME, totp_confirmed: true, password_step_up: false });
    render(
      <QueryClientProvider client={qc}>
        <StepUpDialog onClose={() => {}} onProved={() => {}} />
      </QueryClientProvider>,
    );

    expect(await screen.findByLabelText('Authenticator or recovery code')).toBeInTheDocument();
    expect(screen.queryByLabelText('Password')).not.toBeInTheDocument();
  });
});
