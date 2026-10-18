import { useState } from 'react';
import { describe, expect, it } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { StepsEditor } from './StepsEditor.tsx';
import type { AlertRouteStep } from '../../api/types.ts';
import type { RouteDialogChannel, RouteDialogRotation } from './RouteDialog.tsx';

const CHANNELS: RouteDialogChannel[] = [
  { id: 'c1', name: 'Ops mail' },
  { id: 'c2', name: 'Ops SMS' },
];
const ROTATIONS: RouteDialogRotation[] = [{ id: 'r1', name: 'DB team' }];

/** Harness mirrors how RouteDialog actually holds StepsEditor's state —
 * controlled, with the array it's handed back the only source of truth —
 * and surfaces that array as JSON so a test can assert on the exact shape
 * committed, not just what rendered. */
function Harness({ initial = [] as AlertRouteStep[] }: { initial?: AlertRouteStep[] }) {
  const [steps, setSteps] = useState<AlertRouteStep[]>(initial);
  return (
    <div>
      <StepsEditor steps={steps} onChange={setSteps} channels={CHANNELS} rotations={ROTATIONS} />
      <pre data-testid="steps-json">{JSON.stringify(steps)}</pre>
    </div>
  );
}

function committed(): AlertRouteStep[] {
  return JSON.parse(screen.getByTestId('steps-json').textContent ?? '[]') as AlertRouteStep[];
}

describe('StepsEditor', () => {
  it('appends the next position when a rung is added', async () => {
    render(<Harness />);
    await userEvent.click(screen.getByRole('button', { name: /add rung/i }));
    expect(committed()).toHaveLength(1);
    expect(committed()[0].position).toBe(1);

    await userEvent.click(screen.getByRole('button', { name: /add rung/i }));
    expect(committed()).toHaveLength(2);
    expect(committed().map((s) => s.position)).toEqual([1, 2]);
  });

  it('renumbers the remaining rungs with no gap when a middle one is removed', async () => {
    const initial: AlertRouteStep[] = [
      { position: 1, delay_seconds: 300, channel_ids: ['c1'] },
      { position: 2, delay_seconds: 600, channel_ids: ['c1'] },
      { position: 3, delay_seconds: 1200, channel_ids: ['c2'] },
    ];
    render(<Harness initial={initial} />);

    const rows = screen.getAllByRole('listitem');
    expect(rows).toHaveLength(3);
    await userEvent.click(within(rows[1]).getByRole('button', { name: /remove/i }));

    const after = committed();
    expect(after).toHaveLength(2);
    // Positions run 1..2 with no gap — ValidateSteps' own rule — and the
    // surviving rungs are the first and third original ones, identified by
    // their own delay (the step that *was* position 3, not just "whatever
    // ends up in slot 2"), so this would catch an off-by-one that dropped
    // the wrong row instead of the one actually removed.
    expect(after.map((s) => s.position)).toEqual([1, 2]);
    expect(after.map((s) => s.delay_seconds)).toEqual([300, 1200]);
  });

  it('switches a rung between a rotation and a fixed channel list', async () => {
    render(<Harness initial={[{ position: 1, delay_seconds: 300, channel_ids: ['c1'] }]} />);
    const row = screen.getAllByRole('listitem')[0];

    await userEvent.click(within(row).getByRole('radio', { name: /on-call rotation/i }));
    expect(within(row).getByRole('combobox', { name: /rotation/i })).toBeInTheDocument();
    expect(within(row).queryByRole('checkbox', { name: /ops mail/i })).not.toBeInTheDocument();
    // Still exactly one source, never both — the shape the editor must
    // never be able to construct (ValidateSteps' own either/or rule).
    expect(committed()[0].channel_ids).toBeFalsy();

    await userEvent.click(within(row).getByRole('radio', { name: /fixed list/i }));
    expect(within(row).getByRole('checkbox', { name: /ops mail/i })).toBeInTheDocument();
    expect(committed()[0].rotation_id).toBeFalsy();
  });

  it('enters the delay in minutes and stores it in seconds', () => {
    render(<Harness initial={[{ position: 1, delay_seconds: 300, channel_ids: ['c1'] }]} />);
    const row = screen.getAllByRole('listitem')[0];
    const delayInput = within(row).getByLabelText(/wait/i);
    expect(delayInput).toHaveValue(5);

    // A single fireEvent.change stands in for a whole typed value, the
    // same reason FleetFilters' own tag-field test does (its own comment):
    // this is a controlled field whose displayed value is clamped by the
    // component (never below 1 minute once committed), so simulating
    // keystroke-by-keystroke typing over a "5" that the component keeps
    // re-displaying would exercise this test's own race against React's
    // render, not the component's own minutes-to-seconds conversion.
    fireEvent.change(delayInput, { target: { value: '12' } });
    expect(committed()[0].delay_seconds).toBe(720);
  });

  it('shows the cumulative time beside each rung, not just its own delay', () => {
    // Rung 1 waits 5 min after the initial delivery; rung 2 waits another
    // 10 after rung 1 actually fires — the reader asking "how late is the
    // second rung" needs 15, not 10 (delay_seconds is relative to the rung
    // before it, not to the alert opening).
    const initial: AlertRouteStep[] = [
      { position: 1, delay_seconds: 300, channel_ids: ['c1'] },
      { position: 2, delay_seconds: 600, channel_ids: ['c1'] },
    ];
    render(<Harness initial={initial} />);
    const rows = screen.getAllByRole('listitem');
    expect(within(rows[0]).getByText(/after 5 min/i)).toBeInTheDocument();
    expect(within(rows[1]).getByText(/after 15 min/i)).toBeInTheDocument();
  });

  it('never leaves a new rung naming neither a rotation nor a channel', async () => {
    render(<Harness />);
    await userEvent.click(screen.getByRole('button', { name: /add rung/i }));
    const [step] = committed();
    expect(step.rotation_id !== undefined || step.channel_ids !== undefined).toBe(true);
  });
});
