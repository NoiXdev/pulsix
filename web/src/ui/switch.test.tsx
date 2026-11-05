import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Switch } from './switch.tsx';
import { Label } from './label.tsx';

describe('Switch', () => {
  it('renders role switch reflecting checked', () => {
    const { rerender } = render(<Switch aria-label="Managed" checked={false} onCheckedChange={() => {}} />);
    expect(screen.getByRole('switch', { name: 'Managed' })).toHaveAttribute('aria-checked', 'false');
    rerender(<Switch aria-label="Managed" checked onCheckedChange={() => {}} />);
    expect(screen.getByRole('switch', { name: 'Managed' })).toHaveAttribute('aria-checked', 'true');
  });

  it('click and Space toggle via onCheckedChange', async () => {
    const user = userEvent.setup();
    const onCheckedChange = vi.fn();
    render(<Switch aria-label="Managed" checked={false} onCheckedChange={onCheckedChange} />);
    const sw = screen.getByRole('switch', { name: 'Managed' });
    await user.click(sw);
    expect(onCheckedChange).toHaveBeenLastCalledWith(true);
    sw.focus();
    await user.keyboard(' ');
    expect(onCheckedChange).toHaveBeenCalledTimes(2);
    await user.keyboard('{Enter}');
    expect(onCheckedChange).toHaveBeenCalledTimes(3);
  });

  it('passes the inverse of checked when on', async () => {
    const user = userEvent.setup();
    const onCheckedChange = vi.fn();
    render(<Switch aria-label="Managed" checked onCheckedChange={onCheckedChange} />);
    await user.click(screen.getByRole('switch'));
    expect(onCheckedChange).toHaveBeenCalledWith(false);
  });

  it('works as a controlled toggle', async () => {
    const user = userEvent.setup();
    function Harness() {
      const [on, setOn] = useState(false);
      return <Switch aria-label="Managed" checked={on} onCheckedChange={setOn} />;
    }
    render(<Harness />);
    const sw = screen.getByRole('switch');
    await user.click(sw);
    expect(sw).toHaveAttribute('aria-checked', 'true');
    await user.click(sw);
    expect(sw).toHaveAttribute('aria-checked', 'false');
  });

  it('disabled does not call onCheckedChange', async () => {
    const user = userEvent.setup();
    const onCheckedChange = vi.fn();
    render(<Switch aria-label="Managed" checked={false} disabled onCheckedChange={onCheckedChange} />);
    const sw = screen.getByRole('switch');
    expect(sw).toBeDisabled();
    await user.click(sw);
    expect(onCheckedChange).not.toHaveBeenCalled();
  });

  it('is labelled by an associated <label htmlFor>', async () => {
    const user = userEvent.setup();
    const onCheckedChange = vi.fn();
    render(
      <>
        <Label htmlFor="managed">Managed</Label>
        <Switch id="managed" checked={false} onCheckedChange={onCheckedChange} />
      </>,
    );
    const sw = screen.getByLabelText('Managed');
    expect(sw).toBe(screen.getByRole('switch'));
    await user.click(screen.getByText('Managed'));
    expect(onCheckedChange).toHaveBeenCalledWith(true);
  });

  it('is not a form control: it takes no name prop', () => {
    // @ts-expect-error name is not a Switch prop
    render(<Switch name="x" aria-label="Managed" checked={false} onCheckedChange={() => {}} />);
    expect(screen.getByRole('switch', { name: 'Managed' })).toBeInTheDocument();
  });
});
