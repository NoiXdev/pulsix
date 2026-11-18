import { useState, type ReactNode } from 'react';
import { useMe } from '../../api/hooks/useMe.ts';
import { useLockNow } from './useLockNow.ts';
import { SetPinDialog } from './SetPinDialog.tsx';

/**
 * useLockAction connects the top bar's lock button (ui/shell.tsx takes it
 * as a prop, src/ui cannot fetch) to the session lock: with a PIN it locks
 * at once, without one it opens the set-a-PIN dialog first. Render
 * `dialog` somewhere in the layout.
 */
export function useLockAction(): { lock: () => void; dialog: ReactNode } {
  const me = useMe();
  const [asking, setAsking] = useState(false);
  const { lockNow, locked: markLocked } = useLockNow({ onMissing: () => setAsking(true), onLocked: () => setAsking(false) });

  function locked() {
    setAsking(false);
    markLocked();
  }

  function lock() {
    if (!me.data?.lock_pin_set) {
      setAsking(true);
      return;
    }
    lockNow();
  }

  return { lock, dialog: <SetPinDialog open={asking} onClose={() => setAsking(false)} onLocked={locked} /> };
}
