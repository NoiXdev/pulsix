import { useState } from 'react';
import { Button } from '../../ui/button.tsx';
import { Pill } from '../../ui/badge.tsx';
import { SettingsCard, SettingsRow } from '../../ui/settings.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useMe } from '../../api/hooks/useMe.ts';
import { useRemoveLockPin, useSetLockPin } from '../../api/hooks/useScreenLock.ts';
import { useLockNow } from '../lock/useLockNow.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { useStepUp } from './stepUp.tsx';
import { PinDialog } from './PinDialog.tsx';

/**
 * SessionCard is Account security's "Session" card: the lock PIN (set,
 * change, remove) and this session, with a Lock now when a PIN is set. The
 * server asks for a recently proven factor to replace or remove a PIN (not
 * to set the first one); useStepUp re-asks and repeats the action. Lock
 * now locks the way the top bar does (useLockNow).
 */
export function SessionCard() {
  const t = useT();
  const fmt = useFormat();
  const toast = useToast();
  const me = useMe({ enabled: true });
  const setPin = useSetLockPin();
  const removePin = useRemoveLockPin();
  const stepUp = useStepUp();
  const [editing, setEditing] = useState(false);
  // A 409 lock_pin_missing (the PIN was removed in another tab) offers to set one.
  const lock = useLockNow({ onMissing: () => setEditing(true) });
  const hasPin = me.data?.lock_pin_set === true;
  const legacy = me.data?.lock_pin_legacy === true;
  const expires = me.data?.session_expires_at;

  return (
    <SettingsCard
      title={t.account.session.title}
      description={t.account.session.description}
      badge={hasPin ? undefined : <Pill tone="warn">{t.account.session.noPin}</Pill>}>
      <SettingsRow
        label={
          <span className="flex items-center gap-2">
            {t.account.lockPin.title}
            {legacy && <Pill tone="warn">{t.account.lockPin.legacy}</Pill>}
          </span>
        }
        help={legacy ? t.account.lockPin.legacyHelp : t.account.lockPin.help}
      >
        <div className="flex items-center gap-2">
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
                  (err) => toast.show(err instanceof Error && err.message ? err.message : t.account.lockPin.removeFailed, 'destructive'),
                )
              }
            >
              {t.account.lockPin.remove}
            </Button>
          )}
        </div>
      </SettingsRow>

      <SettingsRow
        label={t.account.session.thisSession}
        help={expires ? t.account.session.expires(fmt.time(expires, { hour: '2-digit', minute: '2-digit' })) : undefined}
      >
        {hasPin && (
          <Button disabled={lock.pending} onClick={lock.lockNow}>
            {t.account.session.lockNow}
          </Button>
        )}
      </SettingsRow>

      <PinDialog
        open={editing}
        replacing={hasPin}
        onClose={() => setEditing(false)}
        onSubmit={async (pin) => {
          let failure: unknown = null;
          // stepUp.run reports every failure but the step-up refusal to its
          // handler; PinForm shows the failure inline.
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
      />
      {stepUp.dialog}
    </SettingsCard>
  );
}
