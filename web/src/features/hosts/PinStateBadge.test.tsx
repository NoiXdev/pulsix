import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { PinStateBadge } from './PinStateBadge.tsx';

describe('PinStateBadge', () => {
  it('labels a full pin as complete', () => {
    render(<PinStateBadge state="full" />);
    expect(screen.getByText('pin complete')).toBeInTheDocument();
  });

  it('labels a fingerprint-only pin as needing confirmation', () => {
    render(<PinStateBadge state="fingerprint_only" />);
    expect(screen.getByText('pin needs confirmation')).toBeInTheDocument();
  });

  it('renders nothing while the host is unpinned or the state is unknown', () => {
    const { container, rerender } = render(<PinStateBadge />);
    expect(container).toBeEmptyDOMElement();
    rerender(<PinStateBadge state="something-else" />);
    expect(container).toBeEmptyDOMElement();
  });
});
