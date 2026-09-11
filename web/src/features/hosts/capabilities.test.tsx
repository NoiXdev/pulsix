import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { CapabilityCell } from './capabilities.tsx';

describe('CapabilityCell', () => {
  it('renders "Ansible · user@ · key" when managed', () => {
    render(<CapabilityCell kind="managed" on detail="user@ · key" onChange={vi.fn()} />);
    expect(screen.getByText('Ansible · user@ · key')).toBeInTheDocument();
  });

  it('renders "off · enable" when not managed', () => {
    render(<CapabilityCell kind="managed" on={false} onChange={vi.fn()} />);
    expect(screen.getByRole('button', { name: 'off · enable' })).toBeInTheDocument();
  });

  it('clicking "enable" calls the handler with {managed: true} only', async () => {
    const onChange = vi.fn();
    render(<CapabilityCell kind="managed" on={false} onChange={onChange} />);

    await userEvent.click(screen.getByRole('button', { name: 'off · enable' }));

    expect(onChange).toHaveBeenCalledExactlyOnceWith({ managed: true });
  });

  it('clicking "enable" on the monitored cell calls the handler with {monitored: true} only', async () => {
    const onChange = vi.fn();
    render(<CapabilityCell kind="monitored" on={false} onChange={onChange} />);

    await userEvent.click(screen.getByRole('button', { name: 'off · enable' }));

    expect(onChange).toHaveBeenCalledExactlyOnceWith({ monitored: true });
  });

  it('renders just "Agent" with no separator when monitored is on but no detail is given', () => {
    render(<CapabilityCell kind="monitored" on onChange={vi.fn()} />);
    expect(screen.getByText('Agent')).toBeInTheDocument();
  });
});
