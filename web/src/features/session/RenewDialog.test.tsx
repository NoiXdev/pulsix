import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi } from '../../test/mockApi.ts';
import { RenewDialog } from './RenewDialog.tsx';
import { meQueryKey } from '../../api/hooks/useMe.ts';

const ME = {
  user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
  mfa_complete: true,
  mfa_enrolment_required: false,
  totp_confirmed: false,
  instance_admin: false,
  session_expires_at: '2026-01-01T12:00:00Z',
};

function renderDialog(me: object, onRenewed = vi.fn()) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(meQueryKey, me);
  render(
    <QueryClientProvider client={qc}>
      <RenewDialog expiresAtMs={Date.now() + 60_000} onRenewed={onRenewed} onLater={() => {}} />
    </QueryClientProvider>,
  );
  return onRenewed;
}

describe('RenewDialog with the second factor optional', () => {
  it('renews by password alone when the password is the account step-up', async () => {
    const renew = vi.fn(() => ({ session_expires_at: '2026-01-02T00:00:00Z' }));
    mockApi({
      'GET /api/v1/auth/me': { ...ME, password_step_up: true },
      'POST /api/v1/auth/session/renew': renew,
    });
    const onRenewed = renderDialog({ ...ME, password_step_up: true });

    // The password is all it asks for: no code field.
    await waitFor(() => expect(screen.queryByLabelText(/code/i)).not.toBeInTheDocument());
    await userEvent.type(screen.getByLabelText('Password'), 'correct horse battery');
    await userEvent.click(screen.getByRole('button', { name: 'Renew session' }));

    await waitFor(() => expect(onRenewed).toHaveBeenCalledTimes(1));
    expect(renew).toHaveBeenCalledWith(expect.objectContaining({ body: { password: 'correct horse battery' } }));
  });

  it('still asks for the code when the account has a second factor', async () => {
    mockApi({});
    renderDialog({ ...ME, totp_confirmed: true, password_step_up: false });

    expect(await screen.findByLabelText(/code/i)).toBeInTheDocument();
  });
});
