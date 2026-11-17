import { useState, type FormEvent } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { useLockSession, useSetLockPin } from '../../api/hooks/useScreenLock.ts';
import { useT } from '../../i18n/useT.ts';

const PIN_SHAPE = /^[0-9]{4,8}$/;

/**
 * SetPinDialog asks for a PIN twice, sets it and then locks the session —
 * the way the top bar's lock button goes when the account has no PIN yet.
 * The shape is checked here as well as on the server (4 to 8 digits) so a
 * typo costs no request. `onLocked` is called once the session is locked.
 */
export function SetPinDialog({ open, onClose, onLocked }: { open: boolean; onClose: () => void; onLocked: () => void }) {
  const t = useT();
  const setPin = useSetLockPin();
  const lock = useLockSession();
  const [pin, setPinValue] = useState('');
  const [again, setAgain] = useState('');
  const [error, setError] = useState<string | null>(null);
  const busy = setPin.isPending || lock.isPending;

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    if (!PIN_SHAPE.test(pin)) return setError(t.lock.setPin.invalid);
    if (pin !== again) return setError(t.lock.setPin.mismatch);
    setError(null);
    try {
      await setPin.mutateAsync(pin);
      await lock.mutateAsync();
    } catch (err) {
      setError(err instanceof Error && err.message ? err.message : t.lock.setPin.failed);
      return;
    }
    setPinValue('');
    setAgain('');
    onLocked();
  }

  return (
    <Dialog open={open} onClose={onClose} title={t.lock.setPin.title}>
      <form className="flex flex-col gap-3" onSubmit={submit}>
        <p className="text-xs text-muted-foreground">{t.lock.setPin.intro}</p>
        <Label htmlFor="lock-new-pin">{t.lock.setPin.pinLabel}</Label>
        <Input
          id="lock-new-pin"
          type="password"
          inputMode="numeric"
          autoComplete="off"
          autoFocus
          className="font-mono"
          value={pin}
          onChange={(e) => setPinValue(e.target.value)}
        />
        <Label htmlFor="lock-new-pin-again">{t.lock.setPin.repeatLabel}</Label>
        <Input
          id="lock-new-pin-again"
          type="password"
          inputMode="numeric"
          autoComplete="off"
          className="font-mono"
          value={again}
          onChange={(e) => setAgain(e.target.value)}
        />
        <p className="text-xs text-muted-foreground">{t.lock.setPin.hint}</p>
        {error && (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose} disabled={busy}>
            {t.lock.setPin.cancel}
          </Button>
          <Button type="submit" disabled={busy}>
            {t.lock.setPin.submit}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
