import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { STEP_UP_REQUIRED, useStepUp } from './stepUp.tsx';

function Harness({ action, onError }: { action: () => Promise<void>; onError: (err: unknown) => void }) {
  const stepUp = useStepUp();
  return (
    <>
      <button type="button" onClick={() => void stepUp.run(action, onError)}>
        go
      </button>
      {stepUp.dialog}
    </>
  );
}

function renderHarness(action: () => Promise<void>, onError: (err: unknown) => void) {
  mockApi({ 'POST /api/v1/auth/totp/verify': {} });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <ToastProvider>
        <Harness action={action} onError={onError} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

async function prove(user: ReturnType<typeof userEvent.setup>) {
  await user.type(await screen.findByLabelText('Authenticator or recovery code'), '123456');
  await user.click(screen.getByRole('button', { name: 'Confirm' }));
}

describe('useStepUp', () => {
  it('retries the action after the proof', async () => {
    const user = userEvent.setup();
    const action = vi.fn().mockRejectedValueOnce(new Error(STEP_UP_REQUIRED)).mockResolvedValue(undefined);
    const onError = vi.fn();
    renderHarness(action, onError);

    await user.click(screen.getByRole('button', { name: 'go' }));
    await prove(user);

    await vi.waitFor(() => expect(action).toHaveBeenCalledTimes(2));
    expect(onError).not.toHaveBeenCalled();
  });

  // A retry that is refused again used to be rethrown outside any handler:
  // nothing showed and the rejection went unhandled. It reaches the caller's
  // own error display like any other failure now.
  it('hands a retry that is refused again to the caller, and does not prompt twice', async () => {
    const user = userEvent.setup();
    const refused = new Error(STEP_UP_REQUIRED);
    const action = vi.fn().mockRejectedValue(refused);
    const onError = vi.fn();
    renderHarness(action, onError);

    await user.click(screen.getByRole('button', { name: 'go' }));
    await prove(user);

    await vi.waitFor(() => expect(onError).toHaveBeenCalledWith(refused));
    expect(action).toHaveBeenCalledTimes(2);
    expect(onError).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('hands any other failure of the retry to the caller', async () => {
    const user = userEvent.setup();
    const other = new Error('boom');
    const action = vi.fn().mockRejectedValueOnce(new Error(STEP_UP_REQUIRED)).mockRejectedValueOnce(other);
    const onError = vi.fn();
    renderHarness(action, onError);

    await user.click(screen.getByRole('button', { name: 'go' }));
    await prove(user);

    await vi.waitFor(() => expect(onError).toHaveBeenCalledWith(other));
  });
});
