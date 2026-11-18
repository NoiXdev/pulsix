import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { SetPinDialog } from './SetPinDialog.tsx';

function setup() {
  const put = vi.fn(() => ({ ok: true }));
  const lock = vi.fn(() => ({ ok: true }));
  mockApi({ 'PUT /api/v1/auth/lock/pin': put, 'POST /api/v1/auth/lock': lock });
  const onLocked = vi.fn();
  render(
    <QueryClientProvider client={new QueryClient()}>
      <SetPinDialog open onClose={() => {}} onLocked={onLocked} />
    </QueryClientProvider>,
  );
  return { put, lock, onLocked };
}

async function fill(pin: string, again: string) {
  await userEvent.type(screen.getByLabelText('PIN'), pin);
  await userEvent.type(screen.getByLabelText('Repeat the PIN'), again);
}

describe('SetPinDialog', () => {
  it('refuses a mismatch without calling the server', async () => {
    const { put } = setup();
    await fill('1234', '1235');
    expect(await screen.findByText('The PINs do not match. Repeat it once more.')).toBeInTheDocument();
    expect(put).not.toHaveBeenCalled();
  });

  it('sets the PIN, then locks', async () => {
    const { put, lock, onLocked } = setup();
    await fill('1234', '1234');
    await vi.waitFor(() => expect(onLocked).toHaveBeenCalled());
    expect(put).toHaveBeenCalledTimes(1);
    expect(lock).toHaveBeenCalledTimes(1);
    expect(put.mock.invocationCallOrder[0]).toBeLessThan(lock.mock.invocationCallOrder[0]);
  });

  it('sends the PIN as the body', async () => {
    let body: unknown;
    mockApi({
      'PUT /api/v1/auth/lock/pin': (req: MockRequest) => {
        body = req.body;
        return { ok: true };
      },
      'POST /api/v1/auth/lock': { ok: true },
    });
    render(
      <QueryClientProvider client={new QueryClient()}>
        <SetPinDialog open onClose={() => {}} onLocked={() => {}} />
      </QueryClientProvider>,
    );
    await fill('4321', '4321');
    await vi.waitFor(() => expect(body).toEqual({ pin: '4321' }));
  });
});
