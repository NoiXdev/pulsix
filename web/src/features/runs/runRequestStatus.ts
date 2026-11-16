import { useEffect, useState } from 'react';
import type { Tone } from '../../ui/badge.tsx';
import type { RunRequest } from '../../api/hooks/useRunRequests.ts';

/** requestTone colours a run request's status: a pending one waits for
 * someone (the accent), an approved one ran, the others ended without a run. */
export function requestTone(status: RunRequest['status']): Tone {
  if (status === 'pending') return 'accent';
  if (status === 'approved') return 'ok';
  return 'unknown';
}

/** sortRequests puts the pending requests first, the soonest to expire on
 * top, then the rest, newest first. */
export function sortRequests(requests: readonly RunRequest[]): RunRequest[] {
  const pending = requests.filter((r) => r.status === 'pending').sort((a, b) => a.expires_at.localeCompare(b.expires_at));
  const rest = requests.filter((r) => r.status !== 'pending').sort((a, b) => b.created_at.localeCompare(a.created_at));
  return [...pending, ...rest];
}

/** useNow is the current time, refreshed every `intervalMs`, so an expiry
 * countdown moves while the page is open. */
export function useNow(intervalMs = 30_000): Date {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const timer = setInterval(() => setNow(new Date()), intervalMs);
    return () => clearInterval(timer);
  }, [intervalMs]);
  return now;
}
