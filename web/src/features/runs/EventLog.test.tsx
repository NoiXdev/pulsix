import { describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { EventLog } from './EventLog.tsx';
import * as diffs from './eventDiff.ts';
import type { JobEvent } from '../../api/types.ts';

const ev = (seq: number, attempt: number, task: string, over: Partial<JobEvent> = {}): JobEvent => ({
  seq, attempt, at: '2026-09-15T08:00:00Z', host: 'web-01', task, state: 'ok', payload: {}, ...over,
});

describe('EventLog', () => {
  it('shows every attempt of a requeued job in order, each under its own divider', () => {
    // seq is monotonic across attempts; the second attempt's events follow
    // the first's instead of being hidden behind their seqs.
    render(<EventLog events={[ev(1, 1, 'first-a'), ev(2, 1, 'first-b'), ev(3, 2, 'second-a'), ev(4, 2, 'second-b')]} />);
    const log = screen.getByRole('list', { name: 'Run events' });
    const items = within(log).getAllByRole('listitem').map((li) => li.textContent ?? '');
    expect(items.findIndex((t) => t.includes('Attempt 2'))).toBeGreaterThan(items.findIndex((t) => t.includes('first-b')));
    expect(items.findIndex((t) => t.includes('Attempt 2'))).toBeLessThan(items.findIndex((t) => t.includes('second-a')));
    expect(within(log).getByText('second-b')).toBeInTheDocument();
  });

  it('shows no divider for a job that ran once', () => {
    render(<EventLog events={[ev(1, 1, 'only-a'), ev(2, 1, 'only-b')]} />);
    expect(screen.queryByText(/Attempt/)).not.toBeInTheDocument();
  });

  it('labels the first attempt too once the log spans several', () => {
    render(<EventLog events={[ev(1, 1, 'a'), ev(2, 2, 'b')]} />);
    expect(screen.getByText('Attempt 1')).toBeInTheDocument();
    expect(screen.getByText('Attempt 2')).toBeInTheDocument();
  });

  it('shows the display text of a task event under its state line (each ansible event arrives once)', () => {
    render(
      <EventLog
        events={[
          ev(1, 1, 'Install nginx', { state: 'ok', payload: { event: 'runner_on_ok', stdout: 'ok: [web-01] => {"msg": "done"}' } }),
          ev(2, 1, '', { host: '', state: 'stdout', payload: { text: 'PLAY RECAP' } }),
        ]}
      />,
    );
    const log = screen.getByRole('list', { name: 'Run events' });
    expect(within(log).getByText('ok: [web-01] => {"msg": "done"}')).toBeInTheDocument();
    expect(within(log).getByText('Install nginx')).toBeInTheDocument();
    expect(within(log).getByText('PLAY RECAP')).toBeInTheDocument();
  });

  it('shows the differences a task event reports, a loop\'s own event without repeating its items', () => {
    const res = (diff: unknown) => ({ event: 'runner_on_ok', event_data: { res: diff } });
    render(
      <EventLog
        events={[
          ev(1, 1, 'Write config', { state: 'changed', payload: res({ changed: true, diff: { before: 'port 80\n', after: 'port 8080\n', before_header: '/etc/app.conf' } }) }),
          ev(2, 1, 'Template', { state: 'changed', payload: res({ diff: [{ prepared: '--- a\n+++ b\n@@ -1 +1 @@\n-old\n+new-prepared\n' }] }) }),
          ev(3, 1, 'Loop', { state: 'changed', payload: res({ results: [{ diff: { before: 'a\n', after: 'loop-item\n' } }] }) }),
          ev(4, 1, 'Same', { state: 'ok', payload: res({ diff: { before: 'x\n', after: 'x\n' } }) }),
        ]}
      />,
    );
    const log = screen.getByRole('list', { name: 'Run events' });
    const diffs = within(log).getAllByRole('figure', { name: 'Differences' });
    expect(diffs).toHaveLength(2);
    expect(within(diffs[0]).getByText('-port 80')).toBeInTheDocument();
    expect(within(diffs[0]).getByText('+port 8080')).toBeInTheDocument();
    expect(within(diffs[1]).getByText('+new-prepared')).toBeInTheDocument();
    expect(within(log).queryByText('+loop-item')).not.toBeInTheDocument();
  });

  it('does not render the rows already shown again when an event is appended', () => {
    // The log hook keeps each event's object across merges, so a row's props
    // are unchanged by a new event and the memoised row is skipped.
    const spy = vi.spyOn(diffs, 'eventDiffs');
    const first = [ev(1, 1, 'a'), ev(2, 1, 'b')];
    const view = render(<EventLog events={first} />);
    expect(spy).toHaveBeenCalledTimes(2);
    view.rerender(<EventLog events={[...first, ev(3, 1, 'c')]} />);
    expect(spy).toHaveBeenCalledTimes(3);
    spy.mockRestore();
  });
});
