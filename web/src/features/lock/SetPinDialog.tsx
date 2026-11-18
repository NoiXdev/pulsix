import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { useLockSession, useSetLockPin } from '../../api/hooks/useScreenLock.ts';
import { useT } from '../../i18n/useT.ts';
import { PinForm } from './PinForm.tsx';

/**
 * SetPinDialog asks for a PIN twice, sets it and then locks the session —
 * the way the top bar's lock button goes when the account has no PIN yet.
 * The PIN entry and its validation are PinForm's. `onLocked` is called
 * once the session is locked.
 */
export function SetPinDialog({ open, onClose, onLocked }: { open: boolean; onClose: () => void; onLocked: () => void }) {
  const t = useT();
  const setPin = useSetLockPin();
  const lock = useLockSession();
  const busy = setPin.isPending || lock.isPending;

  return (
    <Dialog open={open} onClose={onClose} title={t.lock.setPin.title}>
      <p className="mb-3 text-xs text-muted-foreground">{t.lock.setPin.intro}</p>
      <PinForm
        busy={busy}
        onSubmit={async (pin) => {
          await setPin.mutateAsync(pin);
          await lock.mutateAsync();
          onLocked();
        }}
        extra={
          <Button type="button" variant="ghost" onClick={onClose} disabled={busy}>
            {t.lock.setPin.cancel}
          </Button>
        }
      />
    </Dialog>
  );
}
