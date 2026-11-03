import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { Card } from './table.tsx';

describe('Card', () => {
  // AppShell's <main> is a fixed-height flex column that scrolls. A flex item
  // with overflow other than visible has an automatic minimum size of 0, so a
  // Card without shrink-0 is squeezed below its content and clips its rows.
  it('does not shrink inside a flex column', () => {
    render(<Card>content</Card>);
    expect(screen.getByText('content')).toHaveClass('shrink-0');
  });

  it('keeps the rounded surface and merges a caller class', () => {
    render(<Card className="extra">content</Card>);
    expect(screen.getByText('content')).toHaveClass('rounded-[10px]', 'bg-card', 'extra');
  });
});
