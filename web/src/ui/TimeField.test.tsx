import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { TimeField, type TimeFieldProps } from './TimeField.tsx';
import { I18nProvider } from '../i18n/I18nProvider.tsx';
import type { Locale } from '../i18n/types.ts';

function renderField(over: Partial<TimeFieldProps> = {}, locale: Locale = 'en') {
  const onChange = vi.fn();
  render(
    <I18nProvider locale={locale}>
      <TimeField aria-label="At" value="" onChange={onChange} {...over} />
    </I18nProvider>,
  );
  return { onChange, input: screen.getByRole('textbox', { name: 'At' }) };
}

describe('TimeField', () => {
  it.each([
    ['0930', '09:30'],
    ['9:30', '09:30'],
    ['9:5', '09:05'],
    ['930', '09:30'],
    ['9.30', '09:30'],
    ['9', '09:00'],
    ['23:59', '23:59'],
  ])('reads %s as %s', async (typed, want) => {
    const user = userEvent.setup();
    const { onChange, input } = renderField();
    await user.type(input, typed);
    await user.tab();
    expect(onChange).toHaveBeenLastCalledWith(want);
    expect(input).toHaveValue(want);
    expect(input).not.toHaveAttribute('aria-invalid');
  });

  // Final review #8: a decimal keypad, since iOS's numeric one has no
  // separator key and '9.30' (above) is a time this field reads.
  it('asks touch keyboards for digits with a separator', () => {
    const { input } = renderField();
    expect(input).toHaveAttribute('inputmode', 'decimal');
  });

  it('never emits a partial entry while typing', async () => {
    const user = userEvent.setup();
    const { onChange, input } = renderField();
    await user.type(input, '9:3');
    expect(onChange).not.toHaveBeenCalled();
  });

  it.each(['25:00', '12:60', 'abc', '12345'])('keeps the last value and marks %s invalid', async (typed) => {
    const user = userEvent.setup();
    const { onChange, input } = renderField({ value: '08:00' });
    await user.clear(input);
    onChange.mockClear();
    await user.type(input, typed);
    await user.tab();
    expect(onChange).not.toHaveBeenCalled();
    expect(input).toHaveAttribute('aria-invalid', 'true');
    expect(input).toHaveValue(typed);
  });

  it('gives an empty value when cleared', async () => {
    const user = userEvent.setup();
    const { onChange, input } = renderField({ value: '14:05' });
    await user.clear(input);
    expect(onChange).toHaveBeenLastCalledWith('');
  });

  it.each(['en', 'de'] as const)('shows 14:05 as 14:05 in %s', (locale) => {
    const { input } = renderField({ value: '14:05' }, locale);
    expect(input).toHaveValue('14:05');
  });

  it('follows a value changed from outside', () => {
    const { rerender } = render(<TimeField aria-label="At" value="08:00" onChange={() => {}} />);
    rerender(<TimeField aria-label="At" value="17:45" onChange={() => {}} />);
    expect(screen.getByRole('textbox', { name: 'At' })).toHaveValue('17:45');
  });

  it('blocks a form submit while the entry is invalid', async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn((e: { preventDefault: () => void }) => e.preventDefault());
    function Harness() {
      const [value, setValue] = useState('08:00');
      return (
        <form onSubmit={onSubmit}>
          <TimeField aria-label="At" value={value} onChange={setValue} />
          <button type="submit">Save</button>
        </form>
      );
    }
    render(<Harness />);
    const input = screen.getByRole('textbox', { name: 'At' });
    await user.clear(input);
    await user.type(input, '25:00');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(onSubmit).not.toHaveBeenCalled();

    await user.clear(input);
    await user.type(input, '09:15');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(onSubmit).toHaveBeenCalledTimes(1);
  });

  it('blocks a form submit while required and empty', async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn((e: { preventDefault: () => void }) => e.preventDefault());
    render(
      <form onSubmit={onSubmit}>
        <TimeField aria-label="At" value="" onChange={() => {}} required />
        <button type="submit">Save</button>
      </form>,
    );
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(onSubmit).not.toHaveBeenCalled();
  });
});
