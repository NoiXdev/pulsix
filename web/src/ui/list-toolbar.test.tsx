import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { I18nProvider } from '../i18n/I18nProvider.tsx';
import { ActiveFilter, FilterChip, FilterMenu, ListToolbar, ResetFilters, ResultCount, SearchInput } from './list-toolbar.tsx';

afterEach(() => vi.useRealTimers());

describe('ListToolbar', () => {
  it('renders its children and the count', () => {
    render(
      <ListToolbar count={<span>6 agents</span>}>
        <button type="button">Online</button>
      </ListToolbar>,
    );
    expect(screen.getByRole('button', { name: 'Online' })).toBeInTheDocument();
    expect(screen.getByText('6 agents')).toBeInTheDocument();
  });
});

describe('SearchInput', () => {
  it('is labelled and reports the text debounced, once', () => {
    vi.useFakeTimers();
    const onChange = vi.fn();
    render(<SearchInput value="" onChange={onChange} label="Search agents" placeholder="Filter agents…" />);
    const box = screen.getByRole('searchbox', { name: 'Search agents' });
    expect(box).toHaveAttribute('placeholder', 'Filter agents…');

    fireEvent.change(box, { target: { value: 'w' } });
    fireEvent.change(box, { target: { value: 'we' } });
    act(() => vi.advanceTimersByTime(249));
    expect(onChange).not.toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(1));
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith('we');
    expect(box).toHaveValue('we');
  });

  it('honours a custom delay', () => {
    vi.useFakeTimers();
    const onChange = vi.fn();
    render(<SearchInput value="" onChange={onChange} label="Search" debounceMs={50} />);
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'x' } });
    act(() => vi.advanceTimersByTime(50));
    expect(onChange).toHaveBeenCalledWith('x');
  });

  it('clears at once with the clear button, shown only while there is text, and keeps focus in the box', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<SearchInput value="web" onChange={onChange} label="Search" />);
    await user.click(screen.getByRole('button', { name: 'Clear the search' }));
    expect(onChange).toHaveBeenCalledWith('');
    expect(screen.getByRole('searchbox')).toHaveValue('');
    expect(screen.getByRole('searchbox')).toHaveFocus();
    expect(screen.queryByRole('button', { name: 'Clear the search' })).not.toBeInTheDocument();
  });

  it('follows a change of value from outside', () => {
    const { rerender } = render(<SearchInput value="first" onChange={vi.fn()} label="Search" />);
    rerender(<SearchInput value="other" onChange={vi.fn()} label="Search" />);
    expect(screen.getByRole('searchbox')).toHaveValue('other');
  });

  it('drops a keystroke still waiting when the value changes from outside', () => {
    vi.useFakeTimers();
    const onChange = vi.fn();
    const { rerender } = render(<SearchInput value="" onChange={onChange} label="Search" />);
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'ab' } });
    rerender(<SearchInput value="zz" onChange={onChange} label="Search" />);
    act(() => vi.advanceTimersByTime(500));
    expect(onChange).not.toHaveBeenCalled();
    expect(screen.getByRole('searchbox')).toHaveValue('zz');
  });

  it('ignores the late echo of its own writes', () => {
    vi.useFakeTimers();
    function Harness({ echo }: { echo: string }) {
      return <SearchInput value={echo} onChange={() => {}} label="Search" />;
    }
    const { rerender } = render(<Harness echo="" />);
    const box = screen.getByRole('searchbox');
    fireEvent.change(box, { target: { value: 'ab' } });
    act(() => vi.advanceTimersByTime(250));
    fireEvent.change(box, { target: { value: 'abc' } });
    // The URL only now delivers "ab": the box keeps "abc".
    rerender(<Harness echo="ab" />);
    expect(box).toHaveValue('abc');
  });

  it('round-trips through a parent that holds the value', async () => {
    const user = userEvent.setup();
    function Parent() {
      const [q, setQ] = useState('');
      return (
        <>
          <SearchInput value={q} onChange={setQ} label="Search" debounceMs={0} />
          <output>{q}</output>
        </>
      );
    }
    render(<Parent />);
    await user.type(screen.getByRole('searchbox'), 'web');
    await screen.findByText('web', { selector: 'output' });
    expect(screen.getByRole('searchbox')).toHaveValue('web');
  });
});

describe('FilterChip', () => {
  it('is a toggle button with aria-pressed', async () => {
    const user = userEvent.setup();
    const onToggle = vi.fn();
    const { rerender } = render(
      <FilterChip pressed={false} onToggle={onToggle}>
        Online
      </FilterChip>,
    );
    const chip = screen.getByRole('button', { name: 'Online' });
    expect(chip).toHaveAttribute('aria-pressed', 'false');
    await user.click(chip);
    expect(onToggle).toHaveBeenCalledTimes(1);
    rerender(
      <FilterChip pressed onToggle={onToggle} tone="crit">
        Online
      </FilterChip>,
    );
    expect(chip).toHaveAttribute('aria-pressed', 'true');
    expect(chip.className).toContain('text-crit');
  });
});

describe('FilterMenu', () => {
  it('opens a menu of filters with their hints and picks one', async () => {
    const user = userEvent.setup();
    const onZone = vi.fn();
    render(
      <FilterMenu
        items={[
          { id: 'zone', label: 'Zone', hint: 'default, eu', onSelect: onZone },
          { id: 'version', label: 'Version', onSelect: vi.fn() },
        ]}
      />,
    );
    const trigger = screen.getByRole('button', { name: '+ Filter' });
    expect(trigger).toHaveAttribute('aria-haspopup', 'menu');
    expect(trigger).toHaveAttribute('aria-expanded', 'false');
    await user.click(trigger);

    const menu = await screen.findByRole('menu', { name: 'More filters' });
    expect(trigger).toHaveAttribute('aria-expanded', 'true');
    const items = screen.getAllByRole('menuitem');
    expect(items.map((i) => i.textContent)).toEqual(['Zonedefault, eu', 'Version']);
    expect(menu).toBeInTheDocument();

    await user.click(screen.getByRole('menuitem', { name: /Zone/ }));
    expect(onZone).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });

  it('closes on Escape and goes back to the button', async () => {
    const user = userEvent.setup();
    render(<FilterMenu label="More" items={[{ id: 'a', label: 'A', onSelect: vi.fn() }]} />);
    await user.click(screen.getByRole('button', { name: 'More' }));
    await screen.findByRole('menu');
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'More' })).toHaveFocus();
  });

  it('is not shown without items', () => {
    render(<FilterMenu items={[]} />);
    expect(screen.queryByRole('button')).not.toBeInTheDocument();
  });
});

describe('ActiveFilter', () => {
  it('shows the filter and removes it', async () => {
    const user = userEvent.setup();
    const onRemove = vi.fn();
    render(<ActiveFilter label="Zone: eu" onRemove={onRemove} />);
    expect(screen.getByText('Zone: eu')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Remove filter Zone: eu' }));
    expect(onRemove).toHaveBeenCalledTimes(1);
  });

  it('takes its own remove label', () => {
    render(<ActiveFilter label="Host: web-04" onRemove={vi.fn()} removeLabel="Remove host filter" />);
    expect(screen.getByRole('button', { name: 'Remove host filter' })).toBeInTheDocument();
  });
});

describe('ResetFilters', () => {
  it('resets', async () => {
    const user = userEvent.setup();
    const onReset = vi.fn();
    render(<ResetFilters onReset={onReset} />);
    await user.click(screen.getByRole('button', { name: 'Reset every filter' }));
    expect(onReset).toHaveBeenCalledTimes(1);
    expect(screen.getByRole('button')).toHaveTextContent('Reset');
  });
});

describe('ResultCount', () => {
  const agents = (n: number) => (n === 1 ? '1 agent' : `${n} agents`);

  it('says how many of all are shown', () => {
    render(<ResultCount shown={3} total={6} noun={agents} />);
    expect(screen.getByText('3 of 6 agents')).toBeInTheDocument();
  });

  it('says the total alone when nothing is filtered out or the total is not known', () => {
    const { rerender } = render(<ResultCount shown={6} total={6} noun={agents} />);
    expect(screen.getByText('6 agents')).toBeInTheDocument();
    rerender(<ResultCount shown={1} noun={agents} />);
    expect(screen.getByText('1 agent')).toBeInTheDocument();
  });

  it('speaks German', () => {
    render(
      <I18nProvider locale="de">
        <ResultCount shown={3} total={6} noun={(n) => `${n} Agenten`} />
        <ResetFilters onReset={vi.fn()} />
      </I18nProvider>,
    );
    expect(screen.getByText('3 von 6 Agenten')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Alle Filter zurücksetzen' })).toHaveTextContent('Zurücksetzen');
  });
});
