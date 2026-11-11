import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { DatePicker, type DatePickerProps } from './DatePicker.tsx';
import { Dialog } from './dialog.tsx';
import { Label } from './label.tsx';
import { I18nProvider } from '../i18n/I18nProvider.tsx';
import type { Locale } from '../i18n/types.ts';
import { enUi } from '../i18n/en/ui.ts';
import { deUi } from '../i18n/de/ui.ts';

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

  // Final review #2: a disabled field is barred from validation, so a
  // greyed-out picker never blocks the surrounding form's submit.
  it('does not block a form submit while disabled, whatever its validity', async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn((e: { preventDefault: () => void }) => e.preventDefault());
    render(
      <form onSubmit={onSubmit}>
        <DatePicker aria-label="Start" value="" onChange={() => {}} required disabled invalidMessage="Pick a day" />
        <button type="submit">Save</button>
      </form>,
    );
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(onSubmit).toHaveBeenCalledTimes(1);
  });

  // Backlog 1.50 (1): the browser would focus the hidden validation proxy
  // after a blocked submit, where nobody sees or hears it.
  describe('a blocked submit', () => {
    function Harness({ onSubmit, invalidMessage }: { onSubmit: () => void; invalidMessage?: string }) {
      const [value, setValue] = useState('');
      return (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            onSubmit();
          }}
        >
          <DatePicker aria-label="Start" value={value} onChange={setValue} required invalidMessage={invalidMessage} />
          <button type="submit">Save</button>
        </form>
      );
    }

    // aria-required is not allowed on a button: the trigger says it in
    // its description instead, and its name stays the label.
    it('describes the trigger as required', () => {
      const { trigger } = renderPicker({ required: true });
      expect(trigger).not.toHaveAttribute('aria-required');
      expect(trigger).toHaveAccessibleName('Start');
      expect(trigger).toHaveAccessibleDescription(enUi.picker.requiredHint);
    });

    it('describes the date and then required, with a date chosen', () => {
      const { trigger } = renderPicker({ required: true, value: '2026-11-11' });
      expect(trigger).toHaveAccessibleDescription(`11/11/2026 ${enUi.picker.requiredHint}`);
    });

    it('says Pflichtfeld in German', () => {
      const { trigger } = renderPicker({ required: true }, 'de');
      expect(trigger).toHaveAccessibleDescription(deUi.picker.requiredHint);
    });

    it('is not described as required while optional', () => {
      const { trigger } = renderPicker();
      expect(trigger).not.toHaveAccessibleDescription();
    });

    it('focuses the trigger and links a message to it, until a day is picked', async () => {
      const user = userEvent.setup();
      const onSubmit = vi.fn();
      render(<Harness onSubmit={onSubmit} />);
      const trigger = screen.getByRole('button', { name: 'Start' });
      expect(trigger).not.toHaveAttribute('aria-invalid');
      expect(screen.queryByText(enUi.picker.required)).not.toBeInTheDocument();

      await user.click(screen.getByRole('button', { name: 'Save' }));
      expect(onSubmit).not.toHaveBeenCalled();
      expect(trigger).toHaveFocus();
      expect(trigger).toHaveAttribute('aria-invalid', 'true');
      expect(trigger).toHaveAccessibleDescription(`${enUi.picker.requiredHint} ${enUi.picker.required}`);

      await user.click(trigger);
      await user.click(day(14));
      expect(trigger).not.toHaveAttribute('aria-invalid');
      expect(screen.queryByText(enUi.picker.required)).not.toBeInTheDocument();
      await user.click(screen.getByRole('button', { name: 'Save' }));
      expect(onSubmit).toHaveBeenCalledTimes(1);
    });

    it("shows the parent's message when it has one", async () => {
      const user = userEvent.setup();
      render(<Harness onSubmit={() => {}} invalidMessage="Pick a day for that time" />);
      await user.click(screen.getByRole('button', { name: 'Save' }));
      const trigger = screen.getByRole('button', { name: 'Start' });
      expect(trigger).toHaveFocus();
      expect(trigger).toHaveAccessibleDescription(`${enUi.picker.requiredHint} Pick a day for that time`);
    });

    // A browser focuses the first invalid control whose `invalid` event
    // was not cancelled, even one after the picker; jsdom focuses nothing,
    // so the later field does here what the browser would.
    it('takes the focus back from a later invalid field', async () => {
      const user = userEvent.setup();
      render(
        <form onSubmit={(e) => e.preventDefault()}>
          <DatePicker aria-label="Start" value="" onChange={() => {}} required />
          <input aria-label="Note" required onInvalid={(e) => e.currentTarget.focus()} />
          <button type="submit">Save</button>
        </form>,
      );
      await user.click(screen.getByRole('button', { name: 'Save' }));
      await waitFor(() => expect(screen.getByRole('button', { name: 'Start' })).toHaveFocus());
    });

    it('leaves the focus to an earlier invalid field', async () => {
      const user = userEvent.setup();
      render(
        <form onSubmit={(e) => e.preventDefault()}>
          <input aria-label="Name" required />
          <DatePicker aria-label="Start" value="" onChange={() => {}} required />
          <button type="submit">Save</button>
        </form>,
      );
      await user.click(screen.getByRole('button', { name: 'Save' }));
      expect(screen.getByRole('button', { name: 'Start' })).not.toHaveFocus();
      expect(screen.getByRole('button', { name: 'Start' })).toHaveAccessibleDescription(`${enUi.picker.requiredHint} ${enUi.picker.required}`);
    });
  });

  // Backlog 1.50 (2).
  describe('closing', () => {
    it('stays open, unmoved, when the field label is clicked', async () => {
      const user = userEvent.setup();
      const onChange = vi.fn();
      render(
        <>
          <Label htmlFor="start">Starts on</Label>
          <DatePicker id="start" value="2026-11-11" onChange={onChange} />
        </>,
      );
      const trigger = screen.getByRole('button', { name: 'Starts on' });
      await user.click(trigger);
      const grid = screen.getByRole('grid');
      await user.click(screen.getByText('Starts on'));
      // The same grid: not closed on mousedown and opened again on click.
      expect(screen.getByRole('grid')).toBe(grid);
      expect(trigger).toHaveAttribute('aria-expanded', 'true');
      expect(onChange).not.toHaveBeenCalled();
    });

    // Safari does not focus a button on click: without the picker's own
    // focus() the closed calendar would leave focus on <body>.
    it('gives focus back to the trigger when the trigger closes it', async () => {
      const user = userEvent.setup();
      const { trigger } = renderPicker({ value: '2026-11-11' });
      await user.tab();
      await user.keyboard('{Enter}');
      expect(day(11)).toHaveFocus();
      fireEvent.click(trigger);
      expect(screen.queryByRole('grid')).not.toBeInTheDocument();
      expect(trigger).toHaveFocus();
    });

    it('gives focus back to the trigger when a day is clicked', async () => {
      const user = userEvent.setup();
      const { trigger } = renderPicker({ value: '2026-11-11' });
      await user.tab();
      await user.keyboard('{Enter}');
      fireEvent.click(day(14));
      expect(screen.queryByRole('grid')).not.toBeInTheDocument();
      expect(trigger).toHaveFocus();
    });
  });

  // Backlog 1.50 (4): the calendar speaks the app's words, not the
  // library's, so an upgrade cannot rename them under the e2e.
  describe('its own labels', () => {
    it.each([
      ['en', enUi, 'Wednesday, November 11, 2026', 'Saturday, November 14, 2026'],
      ['de', deUi, 'Mittwoch, 11. November 2026', 'Samstag, 14. November 2026'],
    ] as const)('names the %s navigation and days from the catalogue', async (locale, ui, eleventh, fourteenth) => {
      vi.useFakeTimers({ toFake: ['Date'] });
      vi.setSystemTime(new Date(2026, 10, 11, 9, 0));
      const user = userEvent.setup();
      const { trigger } = renderPicker({ value: '2026-11-11' }, locale);
      await user.click(trigger);
      const calendar = screen.getByRole('dialog', { name: ui.picker.calendar });
      expect(within(calendar).getByRole('navigation', { name: ui.picker.monthNav })).toBeInTheDocument();
      expect(within(calendar).getByRole('button', { name: ui.picker.previousMonth })).toBeInTheDocument();
      expect(within(calendar).getByRole('button', { name: ui.picker.nextMonth })).toBeInTheDocument();
      expect(day(11)).toHaveAccessibleName(ui.picker.daySelected(ui.picker.dayToday(eleventh)));
      expect(day(14)).toHaveAccessibleName(fourteenth);
    });
  });
});
