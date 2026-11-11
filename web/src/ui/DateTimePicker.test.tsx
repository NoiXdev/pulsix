import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { DateTimePicker, type DateTimePickerProps } from './DateTimePicker.tsx';
import { I18nProvider } from '../i18n/I18nProvider.tsx';

function renderPicker(over: Partial<DateTimePickerProps> = {}) {
  const onChange = vi.fn();
  render(<DateTimePicker aria-label="Starts" value="" onChange={onChange} {...over} />);
  return {
    onChange,
    trigger: screen.getByRole('button', { name: 'Starts' }),
    time: screen.getByRole('textbox', { name: 'Starts, time' }),
  };
}

function day(n: number): HTMLElement {
  const cell = within(screen.getByRole('grid')).getByText(String(n));
  return cell.closest('button') ?? cell;
}

describe('DateTimePicker', () => {
  it('shows the date and the time of the value', () => {
    render(
      <I18nProvider locale="de">
        <DateTimePicker aria-label="Starts" value="2026-11-11T14:30" onChange={() => {}} />
      </I18nProvider>,
    );
    expect(screen.getByRole('button', { name: 'Starts' })).toHaveTextContent('11.11.2026');
    expect(screen.getByRole('textbox', { name: 'Starts, Uhrzeit' })).toHaveValue('14:30');
  });

  it('keeps the time when a day is picked', async () => {
    const user = userEvent.setup();
    const { onChange, trigger } = renderPicker({ value: '2026-11-11T14:30' });
    await user.click(trigger);
    await user.click(day(14));
    expect(onChange).toHaveBeenCalledWith('2026-11-14T14:30');
  });

  it('keeps the date when the time changes', async () => {
    const user = userEvent.setup();
    const { onChange, time } = renderPicker({ value: '2026-11-11T14:30' });
    await user.clear(time);
    await user.type(time, '0930');
    await user.tab();
    expect(onChange).toHaveBeenLastCalledWith('2026-11-11T09:30');
  });

  it('starts a picked day at midnight when there was no value', async () => {
    const user = userEvent.setup();
    const { onChange, trigger } = renderPicker({ value: '' });
    await user.click(trigger);
    await user.click(day(14));
    expect(onChange).toHaveBeenCalledWith(expect.stringMatching(/^\d{4}-\d{2}-14T00:00$/));
  });

  it('uses a time typed before the day was picked', async () => {
    const user = userEvent.setup();
    const { onChange, trigger, time } = renderPicker({ value: '' });
    await user.type(time, '0815');
    await user.tab();
    expect(onChange).not.toHaveBeenCalled();
    await user.click(trigger);
    await user.click(day(14));
    expect(onChange).toHaveBeenCalledWith(expect.stringMatching(/^\d{4}-\d{2}-14T08:15$/));
  });

  // Review Focus 5.
  it('round-trips a value unchanged when the calendar is opened and closed', async () => {
    const user = userEvent.setup();
    const { onChange, trigger, time } = renderPicker({ value: '2026-11-11T14:30' });
    await user.click(trigger);
    await user.keyboard('{Escape}');
    await user.click(time);
    await user.tab();
    expect(onChange).not.toHaveBeenCalled();
    expect(trigger).toHaveTextContent('11/11/2026');
    expect(time).toHaveValue('14:30');
  });

  it('keeps the date on screen while the time is cleared and retyped', async () => {
    const user = userEvent.setup();
    const seen: string[] = [];
    function Harness() {
      const [value, setValue] = useState('2026-11-11T14:30');
      return (
        <DateTimePicker
          aria-label="Starts"
          value={value}
          onChange={(v) => {
            seen.push(v);
            setValue(v);
          }}
        />
      );
    }
    render(<Harness />);
    const time = screen.getByRole('textbox', { name: 'Starts, time' });
    await user.clear(time);
    expect(seen).toEqual(['']);
    expect(screen.getByRole('button', { name: 'Starts' })).toHaveTextContent('11/11/2026');
    await user.type(time, '16:00');
    await user.tab();
    expect(seen[seen.length - 1]).toBe('2026-11-11T16:00');
  });

  it('disables the days outside the date parts of min and max', async () => {
    const user = userEvent.setup();
    const { trigger } = renderPicker({ value: '2026-11-15T10:00', min: '2026-11-10T08:00', max: '2026-11-20T18:00' });
    await user.click(trigger);
    expect(day(9)).toBeDisabled();
    expect(day(21)).toBeDisabled();
  });

  // Fix round 1: half a date-time is '' to the caller, but it is not an
  // empty field — native datetime-local refuses it as badInput, even when
  // the field is optional. So must this.
  describe('half filled, on an optional field', () => {
    function Harness({ initial, onSubmit }: { initial: string; onSubmit: (e: { preventDefault: () => void }) => void }) {
      const [value, setValue] = useState(initial);
      return (
        <form onSubmit={onSubmit}>
          <DateTimePicker aria-label="Starts" value={value} onChange={setValue} />
          <button type="submit">Save</button>
        </form>
      );
    }

    it('blocks submit with a date and no time, until the time is filled', async () => {
      const user = userEvent.setup();
      const onSubmit = vi.fn((e: { preventDefault: () => void }) => e.preventDefault());
      render(<Harness initial="2026-11-11T14:30" onSubmit={onSubmit} />);
      const time = screen.getByRole('textbox', { name: 'Starts, time' });

      await user.clear(time);
      expect(time).toHaveAttribute('aria-invalid', 'true');
      await user.click(screen.getByRole('button', { name: 'Save' }));
      expect(onSubmit).not.toHaveBeenCalled();

      await user.type(time, '16:00');
      expect(time).not.toHaveAttribute('aria-invalid');
      await user.click(screen.getByRole('button', { name: 'Save' }));
      expect(onSubmit).toHaveBeenCalledTimes(1);
    });

    it('blocks submit with a time and no date, until a day is picked', async () => {
      const user = userEvent.setup();
      const onSubmit = vi.fn((e: { preventDefault: () => void }) => e.preventDefault());
      render(<Harness initial="" onSubmit={onSubmit} />);
      const trigger = screen.getByRole('button', { name: 'Starts' });

      await user.type(screen.getByRole('textbox', { name: 'Starts, time' }), '08:15');
      expect(trigger).toHaveAttribute('aria-invalid', 'true');
      await user.click(screen.getByRole('button', { name: 'Save' }));
      expect(onSubmit).not.toHaveBeenCalled();

      await user.click(trigger);
      await user.click(day(14));
      expect(trigger).not.toHaveAttribute('aria-invalid');
      await user.click(screen.getByRole('button', { name: 'Save' }));
      expect(onSubmit).toHaveBeenCalledTimes(1);
    });

    it('submits when both halves are empty', async () => {
      const user = userEvent.setup();
      const onSubmit = vi.fn((e: { preventDefault: () => void }) => e.preventDefault());
      render(<Harness initial="" onSubmit={onSubmit} />);
      await user.click(screen.getByRole('button', { name: 'Save' }));
      expect(onSubmit).toHaveBeenCalledTimes(1);
    });
  });

  it('blocks a form submit while required and empty', async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn((e: { preventDefault: () => void }) => e.preventDefault());
    render(
      <form onSubmit={onSubmit}>
        <DateTimePicker aria-label="Starts" value="" onChange={() => {}} required />
        <button type="submit">Save</button>
      </form>,
    );
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(onSubmit).not.toHaveBeenCalled();
  });
});
