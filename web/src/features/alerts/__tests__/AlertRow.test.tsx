import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { AlertRow } from '../AlertRow.tsx';
import { alertToRowAlert, isAckCovering, muteLabel, severityRank, subjectLabel, type AlertRowAlert } from '../alertRow.ts';
import type { Alert } from '../../../api/types.ts';

const open: AlertRowAlert = {
  id: 'a1',
  hostId: 'h1',
  scope: 'tuple',
  checkKey: 'disk',
  item: '/var',
  metric: 'use_pct',
  openedAt: '2026-10-01T00:00:00Z',
  severity: 'warn',
  peakSeverity: 'warn',
  closedAt: null,
  closedReason: '',
  ackAt: null,
  ackBy: '',
  ackSeverity: '',
  ackNote: '',
  snoozedUntil: null,
  snoozeUntilChange: false,
  wasFlapping: false,
  muted: false,
  muteReason: '',
  mutedUntil: null,
};

describe('AlertRow', () => {
  it('shows open-ness, peak severity and the current tuple state as three facts', () => {
    render(<AlertRow alert={{ ...open, peakSeverity: 'crit', severity: 'warn' }} />);
    expect(screen.getByTestId('alert-open')).toBeInTheDocument();
    expect(screen.getByTestId('alert-peak')).toHaveTextContent(/crit/i);
    expect(screen.getByTestId('alert-current')).toHaveTextContent(/warn/i);
  });

  it('reads "Open" for an alert whose tuple currently reads ok but which decision 6 keeps open (still flapping)', () => {
    // The row this bug would actually hide: closedAt is still null (the
    // alert never closed, exactly what a flapping tuple does per decision
    // 6), so "open" must say Open even though wasFlapping is the only
    // visible trace of why it has not closed. A row that inferred
    // openness from wasFlapping or from severity instead of closedAt
    // would flip this.
    render(<AlertRow alert={{ ...open, wasFlapping: true }} />);
    expect(screen.getByTestId('alert-open')).toHaveTextContent(/open/i);
  });

  it('reads "Closed" once closedAt is set, never "Open"', () => {
    render(<AlertRow alert={{ ...open, closedAt: '2026-10-02T00:00:00Z', closedReason: 'resolved' }} />);
    const openCell = screen.getByTestId('alert-open');
    expect(openCell).toHaveTextContent(/closed/i);
    expect(openCell).not.toHaveTextContent(/^open$/i);
  });

  it('renders an acknowledged alert whose severity has since risen as unacknowledged, with a note that the ack was overtaken', () => {
    // Acknowledged at warn, risen to crit since (design decision 2): the
    // ack must no longer read as covering. A component that only checked
    // `ackAt !== null` (ignoring ackSeverity entirely) would still show
    // "Acknowledged" here and never fail — this is exactly the bug this
    // test must catch.
    render(<AlertRow alert={{ ...open, ackAt: '2026-10-01T01:00:00Z', ackSeverity: 'warn', ackBy: 'u1', severity: 'crit', peakSeverity: 'crit' }} />);
    const ack = screen.getByTestId('alert-ack');
    expect(ack).toHaveTextContent(/unacknowledged/i);
    expect(ack).toHaveTextContent(/overtaken/i);
  });

  it('keeps an acknowledgement covering once the alert improves (crit ack holds through a drop to warn)', () => {
    // The other direction decision 2 names explicitly: acknowledged at
    // crit, the tuple settles to warn — still covered. A row that voided
    // any ack whose stored severity differs from the current one (instead
    // of only a *worsening* one) would wrongly read this as unacknowledged.
    render(<AlertRow alert={{ ...open, ackAt: '2026-10-01T01:00:00Z', ackSeverity: 'crit', ackBy: 'u1', severity: 'warn', peakSeverity: 'crit' }} />);
    const ack = screen.getByTestId('alert-ack');
    expect(ack).toHaveTextContent(/acknowledged/i);
    expect(ack).not.toHaveTextContent(/overtaken/i);
  });

  it('shows an unacknowledged alert plainly, with no overtaken note', () => {
    render(<AlertRow alert={open} />);
    const ack = screen.getByTestId('alert-ack');
    expect(ack).toHaveTextContent(/unacknowledged/i);
    expect(ack).not.toHaveTextContent(/overtaken/i);
  });

  it('shows a muted alert’s reason and end', () => {
    render(<AlertRow alert={{ ...open, muted: true, muteReason: 'maintenance', mutedUntil: '2026-10-08T12:00:00Z' }} />);
    const mute = screen.getByTestId('alert-mute');
    expect(mute).toHaveTextContent(/maintenance/i);
    // The formatted end date must actually be on screen, not just the word
    // "maintenance" — a component that rendered the reason and silently
    // dropped mutedUntil would still pass a looser assertion.
    expect(mute).toHaveTextContent(/2026|Oct/);
  });

  it('shows an unbounded snooze (until the severity changes) with no end date', () => {
    render(<AlertRow alert={{ ...open, muted: true, muteReason: 'snooze', mutedUntil: null, snoozeUntilChange: true }} />);
    const mute = screen.getByTestId('alert-mute');
    expect(mute).toHaveTextContent(/snooze/i);
    expect(mute).toHaveTextContent(/state changes/i);
  });

  it('renders nothing for alert-mute when the alert is not muted', () => {
    render(<AlertRow alert={open} />);
    expect(screen.queryByTestId('alert-mute')).not.toBeInTheDocument();
  });

  it('offers Acknowledge and Snooze on an open alert when the caller can write', async () => {
    const user = userEvent.setup();
    const onAcknowledge = vi.fn();
    const onSnooze = vi.fn();
    render(<AlertRow alert={open} canWrite onAcknowledge={onAcknowledge} onSnooze={onSnooze} />);

    await user.click(screen.getByRole('button', { name: /acknowledge/i }));
    expect(onAcknowledge).toHaveBeenCalledWith(open);

    await user.click(screen.getByRole('button', { name: /snooze/i }));
    expect(onSnooze).toHaveBeenCalledWith(open);
  });

  it('offers no row actions without write access', () => {
    render(<AlertRow alert={open} canWrite={false} onAcknowledge={vi.fn()} onSnooze={vi.fn()} />);
    expect(screen.queryByRole('button', { name: /acknowledge/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /snooze/i })).not.toBeInTheDocument();
  });

  it('offers no acknowledge/snooze actions on a closed alert', () => {
    render(<AlertRow alert={{ ...open, closedAt: '2026-10-02T00:00:00Z', closedReason: 'resolved' }} canWrite />);
    expect(screen.queryByRole('button', { name: /^acknowledge$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^snooze$/i })).not.toBeInTheDocument();
  });

  it('offers Unacknowledge instead of Acknowledge once the ack covers the current severity', async () => {
    const user = userEvent.setup();
    const onUnacknowledge = vi.fn();
    const covered = { ...open, ackAt: '2026-10-01T01:00:00Z', ackSeverity: 'warn' as const, ackBy: 'u1' };
    render(<AlertRow alert={covered} canWrite onUnacknowledge={onUnacknowledge} />);

    expect(screen.queryByRole('button', { name: /^acknowledge$/i })).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: /unacknowledge/i }));
    expect(onUnacknowledge).toHaveBeenCalledWith(covered);
  });
});

describe('alertRow pure helpers', () => {
  it('alertToRowAlert flattens the wire DTO to camelCase and normalises omitted fields', () => {
    const dto: Alert = {
      id: 'a1',
      host_id: 'h1',
      scope: 'tuple',
      check_key: 'disk',
      item: '/var',
      metric: 'use_pct',
      opened_at: '2026-10-01T00:00:00Z',
      severity: 'warn',
      peak_severity: 'crit',
      snooze_until_change: false,
      was_flapping: false,
      muted: false,
    };
    const row = alertToRowAlert(dto);
    expect(row).toMatchObject({
      id: 'a1',
      hostId: 'h1',
      checkKey: 'disk',
      peakSeverity: 'crit',
      severity: 'warn',
      closedAt: null,
      ackAt: null,
      ackSeverity: '',
      snoozedUntil: null,
      muted: false,
      muteReason: '',
      mutedUntil: null,
    });
  });

  it('severityRank orders unknown below warn and warn below crit, the same order rules.StateRank uses', () => {
    expect(severityRank('unknown')).toBeLessThan(severityRank('warn'));
    expect(severityRank('warn')).toBeLessThan(severityRank('crit'));
    expect(severityRank('ok')).toBeLessThan(severityRank('unknown'));
  });

  it('isAckCovering is false with no acknowledgement at all', () => {
    expect(isAckCovering(open)).toBe(false);
  });

  it('isAckCovering compares against the current severity, not the peak', () => {
    // Acknowledged at crit, peak is still crit, but the alert's own current
    // severity has fallen to warn — still covered (decision 2: improvement
    // does not void). Comparing against peakSeverity instead of severity
    // here would also happen to pass (crit >= crit), so this alone would
    // not catch a peak/current mixup — paired with the "worsens" case
    // below, it does.
    const a = { ...open, ackAt: 't', ackSeverity: 'crit' as const, severity: 'warn' as const, peakSeverity: 'crit' as const };
    expect(isAckCovering(a)).toBe(true);
  });

  it('isAckCovering is voided once current severity outranks the stored ack severity', () => {
    const a = { ...open, ackAt: 't', ackSeverity: 'warn' as const, severity: 'crit' as const, peakSeverity: 'crit' as const };
    expect(isAckCovering(a)).toBe(false);
  });

  it('subjectLabel names a host-scope alert "Host", carrying no check/item/metric', () => {
    expect(subjectLabel({ ...open, scope: 'host', checkKey: '', item: '', metric: '' })).toBe('Host');
  });

  it('subjectLabel joins check, item and metric for a tuple-scope alert', () => {
    expect(subjectLabel(open)).toBe('disk · /var · use_pct');
  });

  it('subjectLabel skips an empty item (a whole-host metric)', () => {
    expect(subjectLabel({ ...open, item: '' })).toBe('disk · use_pct');
  });

  it('muteLabel is null when the alert is not muted', () => {
    expect(muteLabel(open)).toBeNull();
  });

  it('muteLabel names an unbounded snooze as "until the state changes"', () => {
    expect(muteLabel({ ...open, muted: true, muteReason: 'snooze', mutedUntil: null })).toMatch(/state changes/i);
  });
});
