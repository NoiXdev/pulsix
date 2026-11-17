import { useState, type FormEvent, type ReactNode } from 'react';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { useT } from '../../i18n/useT.ts';

const PIN_SHAPE = /^[0-9]{4,8}$/;

/**
 * PinForm asks for a PIN twice and hands it to `onSubmit` once the two
 * match and have the right shape (4 to 8 digits) — checked here as well as
 * on the server so a typo costs no request. It is the one place that
 * validation lives: the lock dialog, the sign-in prompt and the account
 * page all use it. `onSubmit` throws to refuse (its message is shown, or
 * the generic one when it has none); `extra` sits beside the submit
 * button (Cancel, Skip).
 */
export function PinForm({
  submitLabel,
  onSubmit,
  busy = false,
  extra,
  idPrefix = 'lock-new-pin',
}: {
  submitLabel: string;
  onSubmit: (pin: string) => Promise<void>;
  busy?: boolean;
  extra?: ReactNode;
  idPrefix?: string;
}) {
  const t = useT();
  const [pin, setPin] = useState('');
  const [again, setAgain] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [working, setWorking] = useState(false);
  const disabled = busy || working;

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (disabled) return;
    if (!PIN_SHAPE.test(pin)) return setError(t.lock.setPin.invalid);
    if (pin !== again) return setError(t.lock.setPin.mismatch);
    setError(null);
    setWorking(true);
    try {
      await onSubmit(pin);
    } catch (err) {
      setError(err instanceof Error && err.message ? err.message : t.lock.setPin.failed);
      return;
    } finally {
      setWorking(false);
    }
    setPin('');
    setAgain('');
  }

  return (
    <form className="flex flex-col gap-3" onSubmit={submit}>
      <Label htmlFor={idPrefix}>{t.lock.setPin.pinLabel}</Label>
      <Input
        id={idPrefix}
        type="password"
        inputMode="numeric"
        autoComplete="off"
        autoFocus
        className="font-mono"
        value={pin}
        onChange={(e) => setPin(e.target.value)}
      />
      <Label htmlFor={`${idPrefix}-again`}>{t.lock.setPin.repeatLabel}</Label>
      <Input
        id={`${idPrefix}-again`}
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
        {extra}
        <Button type="submit" disabled={disabled}>
          {submitLabel}
        </Button>
      </div>
    </form>
  );
}
