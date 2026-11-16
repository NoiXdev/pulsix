import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { StackedBar } from './StackedBar.tsx';

describe('StackedBar', () => {
  it('sizes each part by its share and names every part with its value', () => {
    render(
      <StackedBar
        label="Timing"
        parts={[
          { key: 'a', label: 'connect', value: 25, display: '25 ms' },
          { key: 'b', label: 'wait', value: 75, display: '75 ms' },
        ]}
      />,
    );
    const bar = screen.getByRole('figure', { name: 'Timing' });
    const segments = within(bar).getAllByTestId('stacked-bar-segment');
    expect(segments.map((s) => s.style.width)).toEqual(['25%', '75%']);
    expect(segments[0]).toHaveAttribute('title', 'connect: 25 ms');
    expect(within(bar).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['connect25 ms', 'wait75 ms']);
  });

  it('draws no segment when every part is zero but still lists them', () => {
    render(<StackedBar label="Timing" parts={[{ key: 'a', label: 'transfer', value: 0, display: '0 ms' }]} />);
    expect(screen.queryByTestId('stacked-bar-segment')).not.toBeInTheDocument();
    expect(screen.getByText('transfer')).toBeInTheDocument();
  });

  it('colours a part by its own shade, not its position', () => {
    render(
      <>
        <StackedBar label="A" parts={[{ key: 'x', label: 'x', value: 1, display: '1', shade: 2 }]} />
        <StackedBar label="B" parts={[{ key: 'y', label: 'y', value: 1, display: '1', shade: 0 }, { key: 'x', label: 'x', value: 1, display: '1', shade: 2 }]} />
      </>,
    );
    const [alone, , second] = screen.getAllByTestId('stacked-bar-segment');
    expect(alone.style.backgroundColor).toBe(second.style.backgroundColor);
    expect(alone.style.backgroundColor).toContain('58%');
  });
});
