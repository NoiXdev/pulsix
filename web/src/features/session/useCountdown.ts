import { useEffect, useState } from 'react';
import { serverNow } from '../../lib/serverClock.ts';

function secondsLeft(expiresAtMs: number): number {
  return Math.max(0, Math.ceil((expiresAtMs - serverNow()) / 1000));
}

/** mmss formats a number of seconds as m:ss. */
export function mmss(total: number): string {
  return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, '0')}`;
}

/** useCountdown is the whole seconds left until `expiresAtMs` on the
 * server's clock, updated every second. */
export function useCountdown(expiresAtMs: number): number {
  const [left, setLeft] = useState(() => secondsLeft(expiresAtMs));
  useEffect(() => {
    const id = setInterval(() => setLeft(secondsLeft(expiresAtMs)), 1000);
    return () => clearInterval(id);
  }, [expiresAtMs]);
  return left;
}
