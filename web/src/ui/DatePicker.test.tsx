import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { DatePicker, type DatePickerProps } from './DatePicker.tsx';
import { Dialog } from './dialog.tsx';
import { Label } from './label.tsx';
import { I18nProvider } from '../i18n/I18nProvider.tsx';
import type { Locale } from '../i18n/types.ts';

function renderPicker(over: Partial<DatePickerProps> = {}, locale: Locale = 'en') {
  const onChange = vi.fn();
  render(
    <I18nProvider locale={locale}>
      <DatePicker aria-label="Start" value="" onChange={onChange} {...over} />
    </I18nProvider>,
  );
  return { onChange, trigger: screen.getByRole('button', { name: 'Start' }) };
}

/** The day button with this day-of-month number in the open calendar —
 * outside days are not shown, so the number is unique in the grid. */
function day(n: number): HTMLElement {
  const cell = within(screen.getByRole('grid')).getByText(String(n));
  return cell.closest('button') ?? cell;
}

afterEach(() => {
  vi.useRealTimers();
});

describe('DatePicker', () => {
  it('shows the value as the locale writes a date', () => {
    renderPicker({ value: '2026-11-11' }, 'de');
    expect(screen.getByRole('button', { name: 'Start' })).toHaveTextContent('11.11.2026');
  });

  it('shows the value in the English short date', () => {
    const { trigger } = renderPicker({ value: '2026-11-04' });
    expect(trigger).toHaveTextContent('11/04/2026');
  });

  it('shows the placeholder when empty', () => {
    const { trigger } = renderPicker({ placeholder: 'No end date' });
    expect(trigger).toHaveTextContent('No end date');
  });

  it('carries the id on the trigger, so an external label names it', () => {
    render(
      <>
        <Label htmlFor="start">Starts on</Label>
        <DatePicker id="start" value="2026-11-11" onChange={() => {}} />
      </>,
    );
    expect(screen.getByLabelText('Starts on')).toBe(screen.getByRole('button', { name: 'Starts on' }));
  });

  it('opens on click', async () => {
    const user = userEvent.setup();
    const { trigger } = renderPicker({ value: '2026-11-11' });
    expect(screen.queryByRole('grid')).not.toBeInTheDocument();
    await user.click(trigger);
    expect(screen.getByRole('grid')).toBeInTheDocument();
    expect(trigger).toHaveAttribute('aria-expanded', 'true');
  });

  it.each([['Enter', '{Enter}'], ['Space', ' ']])('opens on %s', async (_name, key) => {
    const user = userEvent.setup();
    renderPicker({ value: '2026-11-11' });
    await user.tab();
    await user.keyboard(key);
    expect(screen.getByRole('grid')).toBeInTheDocument();
  });

  it('picks a day and closes', async () => {
    const user = userEvent.setup();
    const { onChange, trigger } = renderPicker({ value: '2026-11-11' });
    await user.click(trigger);
    await user.click(day(14));
    expect(onChange).toHaveBeenCalledWith('2026-11-14');
    expect(screen.queryByRole('grid')).not.toBeInTheDocument();
  });

  // Review Focus 1: the whole thing works without a mouse.
  it('is usable from the keyboard alone: arrows move, Enter picks, focus returns', async () => {
    const user = userEvent.setup();
    const { onChange, trigger } = renderPicker({ value: '2026-11-11' });
    await user.tab();
    await user.keyboard('{Enter}');
    expect(day(11)).toHaveFocus();
    await user.keyboard('{ArrowRight}{ArrowDown}{Enter}');
    expect(onChange).toHaveBeenCalledWith('2026-11-19');
    expect(trigger).toHaveFocus();
  });

  // Fix round 1: a real browser ignores focus() on an element inside a
  // `visibility: hidden` box, and jsdom does not. react-day-picker focuses
  // the day from an effect, so the popover must already be visible then —
  // checked at the moment of the call, since jsdom would let it through.
  it('is visible at the moment the calendar focuses its day', async () => {
    const user = userEvent.setup();
    const hiddenAtFocus: boolean[] = [];
    const realFocus = HTMLElement.prototype.focus;
    const spy = vi.spyOn(HTMLElement.prototype, 'focus').mockImplementation(function (this: HTMLElement, opts) {
      const pop = this.closest('[role="dialog"]');
      if (pop && this.closest('[role="grid"]')) {
        const chain: Element[] = [];
        for (let el = this.parentElement; el; el = el.parentElement) chain.push(el);
        hiddenAtFocus.push([this, ...chain].some((el) => getComputedStyle(el).visibility === 'hidden'));
      }
      realFocus.call(this, opts);
    });
    try {
      renderPicker({ value: '2026-11-11' });
      await user.tab();
      await user.keyboard('{Enter}');
      expect(hiddenAtFocus.length).toBeGreaterThan(0);
      expect(hiddenAtFocus).not.toContain(true);
    } finally {
      spy.mockRestore();
    }
  });

  it('picks today with the Today button', async () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date(2026, 10, 11, 15, 30));
    const user = userEvent.setup();
    const { onChange, trigger } = renderPicker({ value: '2026-10-01' });
    await user.click(trigger);
    await user.click(screen.getByRole('button', { name: 'Today' }));
    expect(onChange).toHaveBeenCalledWith('2026-11-11');
  });

  it('closes on Escape and gives focus back to the trigger', async () => {
    const user = userEvent.setup();
    const { onChange, trigger } = renderPicker({ value: '2026-11-11' });
    await user.click(trigger);
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('grid')).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
    // Review Focus 5: opening and closing is not a choice.
    expect(onChange).not.toHaveBeenCalled();
  });

  it('closes on a click outside without changing anything', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <>
        <DatePicker aria-label="Start" value="2026-11-11" onChange={onChange} />
        <p>elsewhere</p>
      </>,
    );
    await user.click(screen.getByRole('button', { name: 'Start' }));
    await user.click(screen.getByText('elsewhere'));
    expect(screen.queryByRole('grid')).not.toBeInTheDocument();
    expect(onChange).not.toHaveBeenCalled();
  });

  // Review Focus 1: Escape inside a dialog belongs to the popover first.
  it('closes only the calendar on Escape inside a dialog, and the dialog on the next one', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(
      <Dialog open onClose={onClose} title="Window">
        <DatePicker aria-label="Start" value="2026-11-11" onChange={() => {}} />
      </Dialog>,
    );
    await user.click(screen.getByRole('button', { name: 'Start' }));
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('grid')).not.toBeInTheDocument();
    expect(screen.getByRole('dialog', { name: 'Window' })).toHaveAttribute('open');
    expect(onClose).not.toHaveBeenCalled();

    await user.keyboard('{Escape}');
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it.each([
    ['de', 'Mo'],
    ['en', 'Su'],
  ] as const)('starts the %s week on %s', async (locale, first) => {
    const user = userEvent.setup();
    const { trigger } = renderPicker({ value: '2026-11-11' }, locale);
    await user.click(trigger);
    // The weekday row is aria-hidden: each day button's own name already
    // carries its weekday, so a screen reader would hear it twice.
    expect(screen.getAllByRole('columnheader', { hidden: true })[0]).toHaveTextContent(first);
  });

  it('disables the days outside min and max', async () => {
    const user = userEvent.setup();
    const { onChange, trigger } = renderPicker({ value: '2026-11-15', min: '2026-11-10', max: '2026-11-20' });
    await user.click(trigger);
    expect(day(9)).toBeDisabled();
    expect(day(21)).toBeDisabled();
    expect(day(10)).toBeEnabled();
    expect(day(20)).toBeEnabled();
    await user.click(day(9));
    expect(onChange).not.toHaveBeenCalled();
  });

  it('does not open when disabled', async () => {
    const user = userEvent.setup();
    const { trigger } = renderPicker({ value: '2026-11-11', disabled: true });
    await user.click(trigger);
    expect(screen.queryByRole('grid')).not.toBeInTheDocument();
  });

  it('clears an optional value', async () => {
    const user = userEvent.setup();
    const { onChange, trigger } = renderPicker({ value: '2026-11-11' });
    await user.click(trigger);
    await user.click(screen.getByRole('button', { name: 'Clear' }));
    expect(onChange).toHaveBeenCalledWith('');
  });

  it('offers no Clear while required', async () => {
    const user = userEvent.setup();
    const { trigger } = renderPicker({ value: '2026-11-11', required: true });
    await user.click(trigger);
    expect(screen.queryByRole('button', { name: 'Clear' })).not.toBeInTheDocument();
  });

  // Review Focus 4.
  it('blocks a form submit while required and empty, and allows it once a day is picked', async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn((e: { preventDefault: () => void }) => e.preventDefault());
    function Harness() {
      const [value, setValue] = useState('');
      return (
        <form onSubmit={onSubmit}>
          <DatePicker aria-label="Start" value={value} onChange={setValue} required />
          <button type="submit">Save</button>
        </form>
      );
    }
    render(<Harness />);

    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(onSubmit).not.toHaveBeenCalled();

    await user.click(screen.getByRole('button', { name: 'Start' }));
    await user.click(day(14));
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(onSubmit).toHaveBeenCalledTimes(1);
  });
});
