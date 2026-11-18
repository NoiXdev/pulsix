import { useToast } from '../../ui/toastContext.ts';
import { useLockSession } from '../../api/hooks/useScreenLock.ts';
import { useT } from '../../i18n/useT.ts';
import { postSessionMessage } from './lockChannel.ts';
import { useLockState } from './lockState.ts';

/**
 * useLockNow locks the session (a PIN is set): it tells the other tabs and
 * flips the lock state on success. A 409 `lock_pin_missing` (the PIN was
 * removed in another tab) calls `onMissing`; any other failure toasts.
 */
export function useLockNow({ onMissing, onLocked }: { onMissing: () => void; onLocked?: () => void }): {
  lockNow: () => void;
  /** Marks the session locked here and in the other tabs (after the server did). */
  locked: () => void;
  pending: boolean;
} {
  const t = useT();
  const toast = useToast();
  const { setLocked } = useLockState();
  const lockSession = useLockSession();

  function locked() {
    postSessionMessage({ type: 'locked' });
    setLocked(true);
  }

  function lockNow() {
    lockSession.mutate(undefined, {
      onSuccess: () => {
        onLocked?.();
        locked();
      },
      onError: (err) => {
        if (err instanceof Error && err.message === 'lock_pin_missing') onMissing();
        else toast.show(t.lock.lockFailed, 'destructive');
      },
    });
  }

  return { lockNow, locked, pending: lockSession.isPending };
}
