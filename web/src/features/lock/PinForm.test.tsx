import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { PinForm } from './PinForm.tsx';

function setup(onSubmit: (pin: string) => Promise<void> = async () => undefined) {
  const spy = vi.fn(onSubmit);
  render(<PinForm onSubmit={spy} />);
  return { spy, first: screen.getByLabelText('PIN'), again: screen.getByLabelText('Repeat the PIN') };
}

describe('PinForm', () => {
  it('shows the hint and no submit button', () => {
    setup();
    expect(screen.getByText('4 digits. Avoid 1234 or 0000.')).toBeInTheDocument();
    expect(screen.queryByRole('button')).not.toBeInTheDocument();
  });

  it('moves focus to the second row once the first is full', async () => {
    const { first, again } = setup();
    expect(first).toHaveFocus();
    await userEvent.type(first, '0427');
    expect(again).toHaveFocus();
  });

  it('submits once when the second row matches', async () => {
    const { spy, first, again } = setup();
    await userEvent.type(first, '0427');
    await userEvent.type(again, '0427');
    await vi.waitFor(() => expect(spy).toHaveBeenCalledTimes(1));
    expect(spy).toHaveBeenCalledWith('0427');
  });

  it('on a mismatch shows the copy, clears the second row, keeps the first and does not submit', async () => {
    const { spy, first, again } = setup();
    await userEvent.type(first, '0427');
    await userEvent.type(again, '0428');
    expect(await screen.findByText('The PINs do not match. Repeat it once more.')).toBeInTheDocument();
    expect(again).toHaveValue('');
    expect(first).toHaveValue('0427');
    expect(again).toHaveFocus();
    expect(spy).not.toHaveBeenCalled();
  });

  it('shows the refusal message, clears both rows and focuses the first', async () => {
    const { first, again } = setup(async () => {
      throw new Error('PIN too weak');
    });
    await userEvent.type(first, '0427');
    await userEvent.type(again, '0427');
    expect(await screen.findByText('PIN too weak')).toBeInTheDocument();
    expect(first).toHaveValue('');
    expect(again).toHaveValue('');
    await vi.waitFor(() => expect(first).toHaveFocus());
  });
});
