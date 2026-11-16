import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MultiSelect } from './MultiSelect.tsx';
import type { SelectOption } from './Select.tsx';

const OPTS: SelectOption[] = [
  { value: 'g1', label: 'Berlin' },
  { value: 'g2', label: 'Frankfurt' },
  { value: 'g3', label: 'Tokyo' },
];

function Harness({ initial = [], max, disabled, onChange }: { initial?: string[]; max?: number; disabled?: boolean; onChange?: (v: string[]) => void }) {
  const [value, setValue] = useState(initial);
  return (
    <MultiSelect
      aria-label="Groups"
      placeholder="Add a group"
      options={OPTS}
      value={value}
      max={max}
      disabled={disabled}
      onChange={(v) => {
        setValue(v);
        onChange?.(v);
      }}
    />
  );
}

describe('MultiSelect', () => {
  it('adds a chosen option, then offers only the rest', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);
    await user.click(screen.getByRole('combobox', { name: 'Groups' }));
    await user.click(screen.getByRole('option', { name: 'Frankfurt' }));
    expect(onChange).toHaveBeenLastCalledWith(['g2']);
    const chosen = screen.getByRole('list', { name: 'Groups' });
    expect(within(chosen).getByText('Frankfurt')).toBeInTheDocument();

    await user.click(screen.getByRole('combobox', { name: 'Groups' }));
    await user.keyboard('to{Enter}');
    expect(onChange).toHaveBeenLastCalledWith(['g2', 'g3']);
    await user.click(screen.getByRole('combobox', { name: 'Groups' }));
    expect(screen.getAllByRole('option').map((o) => o.getAttribute('data-value')).filter(Boolean)).toEqual(['g1']);
  });

  it('removes an entry by its button', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness initial={['g1', 'g3']} onChange={onChange} />);
    await user.click(screen.getByRole('button', { name: 'Remove Berlin' }));
    expect(onChange).toHaveBeenLastCalledWith(['g3']);
  });

  it('names an entry the options do not hold by its value', () => {
    render(<Harness initial={['gone']} />);
    expect(screen.getByText('gone (unavailable)')).toBeInTheDocument();
  });

  it('stops offering more once max entries are chosen', () => {
    render(<Harness initial={['g1', 'g2']} max={2} />);
    expect(screen.getByRole('combobox', { name: 'Groups' })).toBeDisabled();
  });

  it('changes nothing while disabled', () => {
    render(<Harness initial={['g1']} disabled />);
    expect(screen.getByRole('combobox', { name: 'Groups' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Remove Berlin' })).toBeDisabled();
  });
});
