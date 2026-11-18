import { useState } from 'react';
import { Card } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useMe } from '../../api/hooks/useMe.ts';
import { useRemoveLockPin, useSetLockPin } from '../../api/hooks/useScreenLock.ts';
import { PinForm } from '../lock/PinForm.tsx';
import { useT } from '../../i18n/useT.ts';
import { useStepUp } from './stepUp.tsx';

/**
 * LockPinCard sets, changes and removes the PIN that unlocks a locked
 * session. The server asks for a recently proven factor to replace or
 * remove a PIN (not to set the first one); useStepUp re-asks and repeats
 * the action, the same way the other cards do.
 */
export function LockPinCard() {
  const t = useT();
  const toast = useToast();
  const me = useMe({ enabled: true });
  const setPin = useSetLockPin();
  const removePin = useRemoveLockPin();
  const stepUp = useStepUp();
  const [editing, setEditing] = useState(false);
  const hasPin = me.data?.lock_pin_set === true;

  return (
    <Card>
      <div className="flex flex-col gap-3 p-4">
        <div className="flex flex-col gap-1">
          <h2 className="text-sm font-semibold">{t.account.lockPin.title}</h2>
          <p className="text-xs text-muted-foreground">{t.account.lockPin.intro}</p>
          <p className="text-xs">{hasPin ? t.account.lockPin.isSet : t.account.lockPin.notSet}</p>
        </div>
        {editing ? (
          <PinForm
            idPrefix="account-lock-pin"
            onSubmit={async (pin) => {
              let failure: unknown = null;
              // stepUp.run reports every failure but the step-up refusal
              // to its handler; PinForm shows the failure inline.
              await stepUp.run(
                async () => {
                  await setPin.mutateAsync(pin);
                  toast.show(t.account.lockPin.saved);
                  setEditing(false);
                },
                (err) => {
                  failure = err;
                  toast.show(err instanceof Error && err.message ? err.message : t.account.lockPin.saveFailed, 'destructive');
                },
              );
              if (failure) throw failure;
            }}
            extra={
              <Button type="button" variant="ghost" onClick={() => setEditing(false)}>
                {t.account.lockPin.cancel}
              </Button>
            }
          />
        ) : (
          <div className="flex gap-2">
            <Button variant="primary" className="whitespace-nowrap" onClick={() => setEditing(true)}>
              {hasPin ? t.account.lockPin.change : t.account.lockPin.set}
            </Button>
            {hasPin && (
              <Button
                variant="ghost"
                disabled={removePin.isPending}
                onClick={() =>
                  void stepUp.run(
                    async () => {
                      await removePin.mutateAsync();
                      toast.show(t.account.lockPin.removed);
                    },
                    (err) =>
                      toast.show(err instanceof Error && err.message ? err.message : t.account.lockPin.removeFailed, 'destructive'),
                  )
                }
              >
                {t.account.lockPin.remove}
              </Button>
            )}
          </div>
        )}
      </div>
      {stepUp.dialog}
    </Card>
  );
}
