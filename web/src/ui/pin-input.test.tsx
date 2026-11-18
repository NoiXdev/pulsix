import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { PinInput } from './pin-input.tsx';

function Harness({
  onComplete,
  onChange,
}: {
  onComplete?: (d: string) => void;
  onChange?: (d: string) => void;
}) {
  const [value, setValue] = useState('');
  return (
    <>
      <label htmlFor="pin">PIN</label>
      <PinInput
        id="pin"
        value={value}
        onChange={(d) => {
          onChange?.(d);
          setValue(d);
        }}
        onComplete={onComplete}
      />
    </>
  );
}

const boxes = (c: HTMLElement) => c.querySelectorAll('[data-pin-box]');
const filled = (c: HTMLElement) =>
  c.querySelectorAll('[data-pin-box][data-filled="true"]');

describe('PinInput', () => {
  it('reports the growing digit string while typing', async () => {
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);
    await userEvent.type(screen.getByLabelText('PIN'), '42');
    expect(onChange).toHaveBeenNthCalledWith(1, '4');
    expect(onChange).toHaveBeenNthCalledWith(2, '42');
  });

  it('keeps digits only and truncates a paste to the length', async () => {
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);
    const input = screen.getByLabelText('PIN');
    await userEvent.click(input);
    await userEvent.paste('12a45');
    expect(onChange).toHaveBeenLastCalledWith('1245');
  });

  it('drops non-digit input', async () => {
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);
    await userEvent.type(screen.getByLabelText('PIN'), 'ab');
    expect(onChange).not.toHaveBeenCalledWith('a');
    expect(screen.getByLabelText('PIN')).toHaveValue('');
  });

  it('fires onComplete once when the value reaches the length', async () => {
    const onComplete = vi.fn();
    const { rerender } = render(<Harness onComplete={onComplete} />);
    await userEvent.type(screen.getByLabelText('PIN'), '1234');
    expect(onComplete).toHaveBeenCalledTimes(1);
    expect(onComplete).toHaveBeenCalledWith('1234');
    rerender(<Harness onComplete={onComplete} />);
    expect(onComplete).toHaveBeenCalledTimes(1);
  });

  it('fires onComplete again after the value was cleared and refilled', async () => {
    const onComplete = vi.fn();
    render(<Harness onComplete={onComplete} />);
    const input = screen.getByLabelText('PIN');
    await userEvent.type(input, '1234');
    await userEvent.clear(input);
    await userEvent.type(input, '5678');
    expect(onComplete).toHaveBeenCalledTimes(2);
    expect(onComplete).toHaveBeenLastCalledWith('5678');
  });

  it('is named by its label and draws one box per digit', async () => {
    const { container } = render(<Harness />);
    expect(boxes(container)).toHaveLength(4);
    expect(filled(container)).toHaveLength(0);
    await userEvent.type(screen.getByLabelText('PIN'), '123');
    expect(filled(container)).toHaveLength(3);
  });

  it('forwards inputRef and supports a custom length', () => {
    const ref = { current: null as HTMLInputElement | null };
    const { container } = render(
      <PinInput
        id="p"
        value="12"
        length={6}
        onChange={() => {}}
        inputRef={ref}
      />,
    );
    expect(ref.current).toBe(container.querySelector('input'));
    expect(boxes(container)).toHaveLength(6);
    expect(filled(container)).toHaveLength(2);
  });

  it('marks the input invalid', () => {
    const { container } = render(
      <PinInput id="p" value="" onChange={() => {}} invalid />,
    );
    expect(container.querySelector('input')).toHaveAttribute(
      'aria-invalid',
      'true',
    );
  });
});

describe('PinInput focus marker', () => {
  it('keeps the last box marked while a full PIN has the focus', async () => {
    const { container } = render(<Harness />);
    await userEvent.type(screen.getByLabelText('PIN'), '1234');
    const marked = container.querySelectorAll('[data-pin-box][data-active="true"]');
    expect(marked).toHaveLength(1);
    expect(marked[0]).toBe(boxes(container)[3]);
  });
});
