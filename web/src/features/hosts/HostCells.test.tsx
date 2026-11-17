import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import type { HostListItem } from '../../api/types.ts';
import { ActiveRuns } from './HostCells.tsx';

describe('ActiveRuns', () => {
  it('shows a dash when an older manager sends no active_runs', () => {
    const host = { id: 'h1', name: 'web-01' } as unknown as HostListItem;
    render(<ActiveRuns org="acme" host={host} />);
    expect(screen.getByText('—')).toBeInTheDocument();
  });
});
