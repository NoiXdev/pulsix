import { useRef, useState, type ReactNode } from 'react';
import { PinInput } from '../../ui/pin-input.tsx';
import { Label } from '../../ui/label.tsx';
import { useT } from '../../i18n/useT.ts';

const PIN_LENGTH = 4;
const SHAKE_MS = 350;

/**
 * PinForm asks for a 4-digit PIN twice, in two rows of boxes, and hands it
 * to `onSubmit` as soon as the second row is full and matches the first —
 * there is no submit button. A mismatch clears the second row and keeps
 * the first, so a typo costs no request. It is the one place that
 * validation lives: the lock dialog, the sign-in prompt and the account
 * page all use it. `onSubmit` throws to refuse (its message is shown, or
 * the generic one when it has none) and both rows clear; `extra` sits
 * below the rows (Cancel, Skip).
 */
export function PinForm({
  onSubmit,
  busy = false,
  extra,
  idPrefix = 'lock-new-pin',
}: {
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
  const [shake, setShake] = useState(false);
  const firstRef = useRef<HTMLInputElement>(null);
  const againRef = useRef<HTMLInputElement>(null);
  const disabled = busy || working;

  function playShake() {
    setShake(true);
    setTimeout(() => setShake(false), SHAKE_MS);
  }

  async function submit(repeated: string) {
    if (disabled || pin.length !== PIN_LENGTH) return;
    if (pin !== repeated) {
      setError(t.lock.setPin.mismatch);
      setAgain('');
      playShake();
      againRef.current?.focus();
      return;
    }
    setError(null);
    setWorking(true);
    try {
      await onSubmit(pin);
    } catch (err) {
      setError(err instanceof Error && err.message ? err.message : t.lock.setPin.failed);
      setPin('');
      setAgain('');
      playShake();
      setWorking(false);
      // The rows are disabled while working; focus once they are enabled.
      setTimeout(() => firstRef.current?.focus(), 0);
      return;
    }
    setWorking(false);
    setPin('');
    setAgain('');
  }

  return (
    <div className="flex flex-col gap-3">
      <Label htmlFor={idPrefix}>{t.lock.setPin.pinLabel}</Label>
      <PinInput
        id={idPrefix}
        value={pin}
        onChange={setPin}
        onComplete={() => againRef.current?.focus()}
        autoFocus
        disabled={disabled}
        autoComplete="new-password"
        aria-describedby={`${idPrefix}-hint`}
        inputRef={firstRef}
      />
      <Label htmlFor={`${idPrefix}-again`}>{t.lock.setPin.repeatLabel}</Label>
      <PinInput
        id={`${idPrefix}-again`}
        value={again}
        onChange={setAgain}
        onComplete={(digits) => void submit(digits)}
        disabled={disabled}
        invalid={error === t.lock.setPin.mismatch}
        shake={shake}
        autoComplete="new-password"
        inputRef={againRef}
      />
      <p id={`${idPrefix}-hint`} className="text-xs text-muted-foreground">
        {t.lock.setPin.hint}
      </p>
      {error && (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      )}
      {extra && <div className="flex justify-end gap-2">{extra}</div>}
    </div>
  );
}
