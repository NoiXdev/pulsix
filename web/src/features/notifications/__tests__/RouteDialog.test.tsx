import { describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
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

describe('RouteDialog delivery window', () => {
  it('offers weekdays and times rather than a cron field', async () => {
    render(<RouteDialog open onClose={vi.fn()} channels={[{ id: 'c1', name: 'Ops mail' }]} rotations={[]} />);
    await userEvent.click(screen.getByRole('switch', { name: /only deliver during/i }));
    expect(screen.getByLabelText(/from/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/until/i)).toBeInTheDocument();
    expect(screen.getAllByRole('checkbox', { name: /mon|tue|wed|thu|fri|sat|sun/i })).toHaveLength(7);
    expect(screen.queryByLabelText(/cron/i)).not.toBeInTheDocument();
  });

  it('offers breakthrough severities only once a window is set', async () => {
    render(<RouteDialog open onClose={vi.fn()} channels={[{ id: 'c1', name: 'Ops mail' }]} rotations={[]} />);
    expect(screen.queryByText(/outside the window too/i)).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole('switch', { name: /only deliver during/i }));
    expect(screen.getByText(/outside the window too/i)).toBeInTheDocument();
  });

  it('only offers breakthrough severities the route itself fires on', async () => {
    // The server rejects a breakthrough severity outside the route's own
    // set; the dialog must not be able to compose that request.
    render(<RouteDialog open onClose={vi.fn()} channels={[{ id: 'c1', name: 'Ops mail' }]} rotations={[]} />);
    await userEvent.click(screen.getByRole('checkbox', { name: /^critical$/i }));
    await userEvent.click(screen.getByRole('switch', { name: /only deliver during/i }));
    const group = screen.getByRole('group', { name: /outside the window too/i });
    expect(within(group).getAllByRole('checkbox')).toHaveLength(1);
  });

  it('switches between a fixed channel list and a rotation', async () => {
    render(
      <RouteDialog open onClose={vi.fn()}
        channels={[{ id: 'c1', name: 'Ops mail' }]}
        rotations={[{ id: 'r1', name: 'DB team' }]} />,
    );
    await userEvent.click(screen.getByRole('radio', { name: /on-call rotation/i }));
    expect(screen.getByRole('combobox', { name: /rotation/i })).toBeInTheDocument();
    expect(screen.queryByRole('checkbox', { name: /ops mail/i })).not.toBeInTheDocument();
  });
});
