import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Segmented } from './segmented.tsx';

const OPTIONS = [
  { value: 'group', label: 'Group' },
  { value: 'host', label: 'Per host' },
] as const;

describe('Segmented', () => {
  it('is a named radio group with the value checked', () => {
    render(<Segmented label="Scope" options={OPTIONS} value="host" onChange={() => undefined} />);
    const group = screen.getByRole('group', { name: 'Scope' });
    expect(group).toBeInTheDocument();
    expect(screen.getByRole('radio', { name: 'Per host' })).toBeChecked();
    expect(screen.getByRole('radio', { name: 'Group' })).not.toBeChecked();
  });

  it('picks a value by click', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Segmented label="Scope" options={OPTIONS} value="group" onChange={onChange} />);
    await user.click(screen.getByText('Per host'));
    expect(onChange).toHaveBeenCalledWith('host');
  });

  it('can be disabled', () => {
    render(<Segmented label="Scope" options={OPTIONS} value="group" onChange={() => undefined} disabled />);
    expect(screen.getByRole('radio', { name: 'Group' })).toBeDisabled();
  });
});
