import { useState, type ReactNode } from 'react';
import { useMe } from '../../api/hooks/useMe.ts';
import { useLockSession } from '../../api/hooks/useScreenLock.ts';
import { useToast } from '../../ui/toastContext.ts';
import { useT } from '../../i18n/useT.ts';
import { postSessionMessage } from './lockChannel.ts';
import { useLockState } from './lockState.ts';
import { SetPinDialog } from './SetPinDialog.tsx';

/**
 * useLockAction connects the top bar's lock button (ui/shell.tsx takes it
 * as a prop, src/ui cannot fetch) to the session lock: with a PIN it locks
 * at once, without one it opens the set-a-PIN dialog first. Render
 * `dialog` somewhere in the layout.
 */
export function useLockAction(): { lock: () => void; dialog: ReactNode } {
  const t = useT();
  const toast = useToast();
  const me = useMe();
  const { setLocked } = useLockState();
  const lockSession = useLockSession();
  const [asking, setAsking] = useState(false);

  function locked() {
    setAsking(false);
    postSessionMessage({ type: 'locked' });
    setLocked(true);
  }

  function lock() {
    if (!me.data?.lock_pin_set) {
      setAsking(true);
      return;
    }
    lockSession.mutate(undefined, {
      onSuccess: locked,
      onError: (err) => {
        // 409 lock_pin_missing: the PIN was removed in another tab.
        if (err instanceof Error && err.message === 'lock_pin_missing') setAsking(true);
        else toast.show(t.lock.lockFailed, 'destructive');
      },
    });
  }

  return { lock, dialog: <SetPinDialog open={asking} onClose={() => setAsking(false)} onLocked={locked} /> };
}
