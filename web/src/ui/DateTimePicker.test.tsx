import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { DateTimePicker, type DateTimePickerProps } from './DateTimePicker.tsx';
import { I18nProvider } from '../i18n/I18nProvider.tsx';
import { enUi } from '../i18n/en/ui.ts';

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

    // Final review #3: Clear on an optional field empties it, rather than
    // leaving the time behind as a half-filled, invalid pair.
    it('clears the time with the day, leaving an empty valid field', async () => {
      const user = userEvent.setup();
      const onSubmit = vi.fn((e: { preventDefault: () => void }) => e.preventDefault());
      render(<Harness initial="2026-11-11T14:30" onSubmit={onSubmit} />);
      const trigger = screen.getByRole('button', { name: 'Starts' });
      const time = screen.getByRole('textbox', { name: 'Starts, time' });

      await user.click(trigger);
      await user.click(screen.getByRole('button', { name: 'Clear' }));
      expect(time).toHaveValue('');
      expect(time).not.toHaveAttribute('aria-invalid');
      expect(trigger).not.toHaveAttribute('aria-invalid');
      await user.click(screen.getByRole('button', { name: 'Save' }));
      expect(onSubmit).toHaveBeenCalledTimes(1);
    });

    // Final review #2: a disabled field is out of the form's say, whatever
    // half is still in it.
    it('does not block submit with half a value while disabled', async () => {
      const user = userEvent.setup();
      const onSubmit = vi.fn((e: { preventDefault: () => void }) => e.preventDefault());
      function Toggling() {
        const [value, setValue] = useState('');
        const [off, setOff] = useState(false);
        return (
          <form onSubmit={onSubmit}>
            <DateTimePicker aria-label="Starts" value={value} onChange={setValue} disabled={off} />
            <button type="button" onClick={() => setOff(true)}>Off</button>
            <button type="submit">Save</button>
          </form>
        );
      }
      render(<Toggling />);
      await user.type(screen.getByRole('textbox', { name: 'Starts, time' }), '08:15');
      expect(screen.getByRole('button', { name: 'Starts' })).toHaveAttribute('aria-invalid', 'true');

      await user.click(screen.getByRole('button', { name: 'Off' }));
      expect(screen.getByRole('button', { name: 'Starts' })).not.toHaveAttribute('aria-invalid');
      await user.click(screen.getByRole('button', { name: 'Save' }));
      expect(onSubmit).toHaveBeenCalledTimes(1);
    });

    // Backlog 1.50 (1): the refusal lands on the date's trigger, with the
    // reason linked to it, not on its invisible validation proxy.
    it('focuses the date with the incomplete message after a blocked submit', async () => {
      const user = userEvent.setup();
      const onSubmit = vi.fn((e: { preventDefault: () => void }) => e.preventDefault());
      render(<Harness initial="" onSubmit={onSubmit} />);
      await user.type(screen.getByRole('textbox', { name: 'Starts, time' }), '08:15');
      await user.click(screen.getByRole('button', { name: 'Save' }));
      const trigger = screen.getByRole('button', { name: 'Starts' });
      expect(onSubmit).not.toHaveBeenCalled();
      expect(trigger).toHaveFocus();
      expect(trigger).toHaveAccessibleDescription(enUi.picker.incomplete);
    });

    // Backlog 1.50 (3): SnoozeDialog's "until the severity changes" greys
    // the field out; a time typed before must not come back with it.
    it('drops a shown half when disabled, without telling the caller', async () => {
      const user = userEvent.setup();
      const onChange = vi.fn();
      function Toggling() {
        const [value, setValue] = useState('');
        const [off, setOff] = useState(false);
        return (
          <>
            <DateTimePicker
              aria-label="Starts"
              value={value}
              onChange={(v) => {
                onChange(v);
                setValue(v);
              }}
              disabled={off}
            />
            <button type="button" onClick={() => setOff((o) => !o)}>Toggle</button>
          </>
        );
      }
      render(<Toggling />);
      const time = screen.getByRole('textbox', { name: 'Starts, time' });
      await user.type(time, '08:15');
      await user.tab();
      expect(time).toHaveValue('08:15');

      await user.click(screen.getByRole('button', { name: 'Toggle' }));
      expect(screen.getByRole('textbox', { name: 'Starts, time' })).toHaveValue('');
      await user.click(screen.getByRole('button', { name: 'Toggle' }));
      expect(screen.getByRole('textbox', { name: 'Starts, time' })).toHaveValue('');
      expect(screen.getByRole('button', { name: 'Starts' })).not.toHaveAttribute('aria-invalid');
      expect(onChange).not.toHaveBeenCalled();
    });

    it('keeps a whole value when disabled', async () => {
      const user = userEvent.setup();
      function Toggling() {
        const [off, setOff] = useState(false);
        return (
          <>
            <DateTimePicker aria-label="Starts" value="2026-11-11T14:30" onChange={() => {}} disabled={off} />
            <button type="button" onClick={() => setOff((o) => !o)}>Toggle</button>
          </>
        );
      }
      render(<Toggling />);
      await user.click(screen.getByRole('button', { name: 'Toggle' }));
      expect(screen.getByRole('textbox', { name: 'Starts, time' })).toHaveValue('14:30');
      expect(screen.getByRole('button', { name: 'Starts' })).toHaveTextContent('11/11/2026');
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

  // Final review #4: with a <Label htmlFor={id}>, the time box is named
  // after that label rather than a bare "Time" that every field shares.
  describe('the time box name', () => {
    it('follows the label of the field when an id is given', () => {
      render(
        <>
          <label htmlFor="from">From</label>
          <DateTimePicker id="from" value="" onChange={() => {}} />
          <label htmlFor="until">Until</label>
          <DateTimePicker id="until" value="" onChange={() => {}} />
        </>,
      );
      expect(screen.getByRole('textbox', { name: 'From Time' })).toBeInTheDocument();
      expect(screen.getByRole('textbox', { name: 'Until Time' })).toBeInTheDocument();
    });

    it('says Uhrzeit in German', () => {
      render(
        <I18nProvider locale="de">
          <label htmlFor="from">Von</label>
          <DateTimePicker id="from" value="" onChange={() => {}} />
        </I18nProvider>,
      );
      expect(screen.getByRole('textbox', { name: 'Von Uhrzeit' })).toBeInTheDocument();
    });

    it('is a bare Time with neither an id nor an aria-label', () => {
      render(<DateTimePicker value="" onChange={() => {}} />);
      expect(screen.getByRole('textbox', { name: 'Time' })).toBeInTheDocument();
    });
  });
});
