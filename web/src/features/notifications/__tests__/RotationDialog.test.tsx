import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { RotationDialog } from '../RotationDialog.tsx';

describe('RotationDialog', () => {
  it('keeps the channel order, because the order is the cycle', async () => {
    const onSave = vi.fn();
    render(
      <RotationDialog
        open
        onClose={vi.fn()}
        onSave={onSave}
        channels={[
          { id: 'a', name: 'Anna' },
          { id: 'b', name: 'Bob' },
        ]}
      />,
    );
    await userEvent.type(screen.getByLabelText(/name/i), 'DB team');
    await userEvent.click(screen.getByRole('button', { name: /add Anna/i }));
    await userEvent.click(screen.getByRole('button', { name: /add Bob/i }));
    await userEvent.click(screen.getByRole('button', { name: /move Bob up/i }));
    await userEvent.click(screen.getByRole('button', { name: /save/i }));

    expect(onSave).toHaveBeenCalledWith(expect.objectContaining({ channels: ['b', 'a'] }));
  });

  it('refuses to save without a name or a channel', async () => {
    render(<RotationDialog open onClose={vi.fn()} channels={[{ id: 'a', name: 'Anna' }]} />);
    expect(screen.getByRole('button', { name: /save/i })).toBeDisabled();
  });
});
