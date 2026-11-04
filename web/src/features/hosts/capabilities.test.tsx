import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { CapabilityCell } from './capabilities.tsx';

describe('CapabilityCell', () => {
  it('shows "Managed" when the host is managed', () => {
    render(<CapabilityCell kind="managed" on />);
    expect(screen.getByText('Managed')).toBeInTheDocument();
  });

  it('shows a muted "not managed" when it is not', () => {
    render(<CapabilityCell kind="managed" on={false} />);
    expect(screen.getByText('not managed')).toBeInTheDocument();
    expect(screen.queryByText('Managed')).not.toBeInTheDocument();
  });

  it('shows "Monitored" and "not monitored" for the monitored kind', () => {
    const { rerender } = render(<CapabilityCell kind="monitored" on />);
    expect(screen.getByText('Monitored')).toBeInTheDocument();
    rerender(<CapabilityCell kind="monitored" on={false} />);
    expect(screen.getByText('not monitored')).toBeInTheDocument();
  });

  it('is read-only: nothing in it can be operated', () => {
    render(
      <>
        <CapabilityCell kind="managed" on={false} />
        <CapabilityCell kind="monitored" on />
      </>,
    );
    expect(screen.queryByRole('button')).not.toBeInTheDocument();
    expect(screen.queryByRole('switch')).not.toBeInTheDocument();
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument();
  });
});
