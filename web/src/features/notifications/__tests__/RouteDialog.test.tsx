import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { RouteDialog } from '../RouteDialog.tsx';

describe('RouteDialog', () => {
  it('groups the seven event kinds into state and operator action', () => {
    render(<RouteDialog open onClose={vi.fn()} channels={[{ id: 'c1', name: 'Ops mail' }]} />);
    // Decision 1 chose all seven kinds; the grouping is what keeps that
    // from being a wall of checkboxes.
    expect(screen.getByRole('group', { name: /state/i })).toBeInTheDocument();
    expect(screen.getByRole('group', { name: /operator action/i })).toBeInTheDocument();
    expect(screen.getAllByRole('checkbox', { name: /opened|worsened|closed/i })).toHaveLength(3);
    expect(
      screen.getAllByRole('checkbox', { name: /acknowledged|voided|snoozed|unsnoozed/i }),
    ).toHaveLength(4);
  });

  it('refuses to save a route with no channel', async () => {
    const onSave = vi.fn();
    render(<RouteDialog open onClose={vi.fn()} onSave={onSave} channels={[{ id: 'c1', name: 'Ops mail' }]} />);
    await userEvent.type(screen.getByLabelText(/name/i), 'ops');
    await userEvent.click(screen.getByRole('checkbox', { name: /critical/i }));
    await userEvent.click(screen.getByRole('checkbox', { name: /opened/i }));
    expect(screen.getByRole('button', { name: /save/i })).toBeDisabled();
    expect(onSave).not.toHaveBeenCalled();
  });

  it('refuses to save with no severity and no event', async () => {
    render(<RouteDialog open onClose={vi.fn()} channels={[{ id: 'c1', name: 'Ops mail' }]} />);
    await userEvent.type(screen.getByLabelText(/name/i), 'ops');
    await userEvent.click(screen.getByRole('checkbox', { name: /ops mail/i }));
    expect(screen.getByRole('button', { name: /save/i })).toBeDisabled();
  });
});
