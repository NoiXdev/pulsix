import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { RosterStrip } from '../RosterStrip.tsx';

describe('RosterStrip', () => {
  it('names who is on call now and marks a stand-in', () => {
    render(
      <RosterStrip
        shifts={[
          { starts_at: '2026-01-05T09:00:00Z', ends_at: '2026-01-12T09:00:00Z', channel_name: 'Anna', stand_in: false },
          { starts_at: '2026-01-12T09:00:00Z', ends_at: '2026-01-14T09:00:00Z', channel_name: 'Bob', stand_in: true },
        ]}
        now="2026-01-13T00:00:00Z"
      />,
    );
    expect(screen.getByText(/on call now/i)).toBeInTheDocument();
    expect(screen.getByText('Bob')).toBeInTheDocument();
    expect(screen.getByText(/stand-in/i)).toBeInTheDocument();
  });

  it('says so when the rotation covers nobody', () => {
    render(<RosterStrip shifts={[]} now="2026-01-13T00:00:00Z" />);
    expect(screen.getByText(/nobody is on call/i)).toBeInTheDocument();
  });
});
