import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { Sparkline } from './Sparkline.tsx';

function points(container: HTMLElement): string[] {
  return (container.querySelector('polyline')?.getAttribute('points') ?? '').split(' ').filter(Boolean);
}

describe('Sparkline', () => {
  it('draws one vertex per value across the full width', () => {
    const { container } = render(<Sparkline values={[0, 50, 100]} domain={[0, 100]} label="CPU, last 24 h" />);
    expect(screen.getByRole('img', { name: 'CPU, last 24 h' })).toBeInTheDocument();
    expect(points(container)).toEqual(['0,30', '50,15', '100,0']);
  });

  it('scales to the data when no domain is given, and a flat series sits in the middle', () => {
    const { container, rerender } = render(<Sparkline values={[1, 3]} label="load" />);
    expect(points(container)).toEqual(['0,30', '100,0']);
    rerender(<Sparkline values={[2, 2, 2]} label="load" />);
    expect(points(container)).toEqual(['0,15', '50,15', '100,15']);
  });

  it('draws no line for fewer than two values but keeps its place', () => {
    const { container } = render(<Sparkline values={[4]} label="load" />);
    expect(container.querySelector('polyline')).toBeNull();
    expect(screen.getByRole('img', { name: 'load' })).toBeInTheDocument();
  });
});
