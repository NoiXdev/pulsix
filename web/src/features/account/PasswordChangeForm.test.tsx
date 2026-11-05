import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { PasswordChangeForm } from './PasswordChangeForm.tsx';

function renderForm(variant: 'own' | 'temporary') {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <ToastProvider>
        <PasswordChangeForm variant={variant} onChanged={() => {}} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

const stepUpRequired = () =>
  new Response(JSON.stringify({ title: 'Forbidden', status: 403, detail: 'step_up_required' }), { status: 403 });

describe('PasswordChangeForm', () => {
  it('offers the current password to the browser on the own variant', () => {
    renderForm('own');
    expect(screen.getByLabelText('Current password')).toHaveAttribute('autocomplete', 'current-password');
  });

  // The temporary password was typed in from a note, not saved: a browser
  // that offers the old password here would fill the wrong one.
  it('does not let the browser autofill a saved password on the temporary variant', () => {
    renderForm('temporary');
    expect(screen.getByLabelText('Temporary password')).toHaveAttribute('autocomplete', 'one-time-code');
  });

  it('shows the refusal when the retry after the second-factor proof fails again', async () => {
    const user = userEvent.setup();
    mockApi({
      'POST /api/v1/auth/totp/verify': {},
      'POST /api/v1/auth/password': stepUpRequired,
    });
    renderForm('own');

    await user.type(screen.getByLabelText('Current password'), 'my-old-long-password');
    await user.type(screen.getByLabelText('New password'), 'my-new-long-password');
    await user.type(screen.getByLabelText('Repeat the new password'), 'my-new-long-password');
    await user.click(screen.getByRole('button', { name: 'Change password' }));
    await user.type(await screen.findByLabelText('Authenticator or recovery code'), '123456');
    await user.click(screen.getByRole('button', { name: 'Confirm' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Confirming your identity did not work. Try again.',
    );
  });

  // Only the server's own code means the length rule (Task 12): any other
  // 422 says something else, and the form shows that instead.
  it.each([
    ['password_too_short', 'at least 12 characters'],
    ['the password is on a list of breached passwords', 'the password is on a list of breached passwords'],
  ])('reads a 422 %s for what it says', async (detail, want) => {
    const user = userEvent.setup();
    mockApi({
      'POST /api/v1/auth/password': () =>
        new Response(JSON.stringify({ title: 'Unprocessable Entity', status: 422, detail }), { status: 422 }),
    });
    renderForm('own');

    await user.type(screen.getByLabelText('Current password'), 'my-old-long-password');
    await user.type(screen.getByLabelText('New password'), 'my-new-long-password');
    await user.type(screen.getByLabelText('Repeat the new password'), 'my-new-long-password');
    await user.click(screen.getByRole('button', { name: 'Change password' }));

    expect(await screen.findByText(want)).toBeInTheDocument();
  });
});
