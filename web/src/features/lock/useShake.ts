import { useCallback, useEffect, useRef, useState } from 'react';

const SHAKE_MS = 350;

/** useShake returns a flag that is true for a short moment after `play()`;
 * a new play restarts it and unmounting cancels it. */
export function useShake(): [boolean, () => void] {
  const [shake, setShake] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  const play = useCallback(() => {
    clearTimeout(timer.current);
    setShake(true);
    timer.current = setTimeout(() => setShake(false), SHAKE_MS);
  }, []);
  return [shake, play];
}
