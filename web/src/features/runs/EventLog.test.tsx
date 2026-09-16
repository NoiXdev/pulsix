import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { EventLog } from './EventLog.tsx';
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
});
