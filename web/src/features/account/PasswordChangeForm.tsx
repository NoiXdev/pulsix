import { useState, type FormEvent } from 'react';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Field } from '../../ui/field.tsx';
import { useChangePassword } from '../../api/hooks/usePassword.ts';
import { ApiError } from '../../api/client.ts';
import { MIN_PASSWORD_LENGTH, newPasswordProblem } from '../../lib/password.ts';
import { useT } from '../../i18n/useT.ts';
import type { Messages } from '../../i18n/types.ts';
import { isStepUpRequired, STEP_UP_REQUIRED, useStepUp } from './stepUp.tsx';

/** The refusals of POST /auth/password the form explains in words
 * (internal/api/password_handlers.go). A policy refusal is any other 422. */
const INVALID_CURRENT_PASSWORD = 'invalid_current_password';
const TEMPORARY_PASSWORD_EXPIRED = 'temporary_password_expired';
const SAME_PASSWORD = 'same_password';

/** What is wrong with the new password, shown below its field. */
type FieldProblem = 'tooShort' | 'mismatch' | 'same';

/**
 * PasswordChangeForm changes the signed-in account's own password: the
 * current one, and the new one typed twice. It serves the Password card on
 * Account security (`variant="own"`) and the screen an account that must
 * replace a temporary password is held on (`variant="temporary"`), where
 * the current password is the temporary one.
 *
 * The server wants a recently proven second factor for this; when the
 * proof is stale, the form asks for it (useStepUp) and then carries on.
 * Every other session of the account ends; this one stays. onChanged runs
 * once the server has the new password, after the fields are emptied.
 */
export function PasswordChangeForm({
  variant,
  onChanged,
}: {
  variant: 'own' | 'temporary';
  onChanged: () => void | Promise<void>;
}) {
  const t = useT();
  const change = useChangePassword();
  const stepUp = useStepUp();
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [repeat, setRepeat] = useState('');
  const [problem, setProblem] = useState<FieldProblem | null>(null);
  const [error, setError] = useState<string | null>(null);
  const p = t.account.password;
  const temporary = variant === 'temporary';

  // The attempt handles its own failures, except the step-up refusal,
  // which useStepUp turns into the second-factor prompt and a retry. A
  // retry that fails again is handed to `refused` by useStepUp.
  async function attempt() {
    try {
      await change.mutateAsync({ current_password: current, new_password: next });
    } catch (err) {
      if (isStepUpRequired(err)) throw err;
      refused(err);
      return;
    } finally {
      change.reset();
    }
    setCurrent('');
    setNext('');
    setRepeat('');
    try {
      await onChanged();
    } catch (err) {
      setError(describeError(t, err, temporary));
    }
  }

  function refused(err: unknown) {
    if (err instanceof ApiError && err.message === SAME_PASSWORD) {
      setProblem('same');
    } else if (err instanceof ApiError && err.status === 422) {
      setProblem('tooShort');
    } else {
      setError(describeError(t, err, temporary));
    }
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    const found = newPasswordProblem(next, repeat) ?? (next === current ? 'same' : null);
    setProblem(found);
    if (found) return;
    void stepUp.run(attempt, refused);
  }

  return (
    <>
      <form onSubmit={submit} className="flex flex-col gap-3" noValidate>
        <Field id={`${variant}-current-password`} label={temporary ? p.temporary : p.current}>
          <Input
            id={`${variant}-current-password`}
            type="password"
            // The temporary password was typed in from a note, not saved:
            // a browser must not offer the old password here.
            autoComplete={temporary ? 'one-time-code' : 'current-password'}
            required
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
          />
        </Field>
        <Field
          id={`${variant}-new-password`}
          label={t.auth.newPassword}
          hint={t.auth.minLengthHint(MIN_PASSWORD_LENGTH)}
          error={fieldError(t, problem, temporary)}
        >
          <Input
            id={`${variant}-new-password`}
            type="password"
            autoComplete="new-password"
            required
            value={next}
            onChange={(e) => setNext(e.target.value)}
          />
        </Field>
        <Field
          id={`${variant}-repeat-password`}
          label={t.auth.repeatNewPassword}
          error={problem === 'mismatch' ? t.validation.passwordsMismatch : null}
        >
          <Input
            id={`${variant}-repeat-password`}
            type="password"
            autoComplete="new-password"
            required
            value={repeat}
            onChange={(e) => setRepeat(e.target.value)}
          />
        </Field>
        {error && (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        )}
        <Button type="submit" variant="primary" className="self-start" disabled={change.isPending}>
          {p.submit}
        </Button>
      </form>
      {/* Outside the form: the dialog holds a form of its own. */}
      {stepUp.dialog}
    </>
  );
}

/** The error below the new password's field; a mismatch shows below the
 * repetition instead. */
function fieldError(t: Messages, problem: FieldProblem | null, temporary: boolean): string | null {
  const p = t.account.password;
  switch (problem) {
    case 'tooShort':
      return t.validation.minChars(MIN_PASSWORD_LENGTH);
    case 'same':
      return temporary ? p.sameAsTemporary : p.sameAsCurrent;
    default:
      return null;
  }
}

/** A refusal that is not about the new password, in the reader's language;
 * anything unexpected is the server's own message. */
function describeError(t: Messages, err: unknown, temporary: boolean): string {
  const p = t.account.password;
  const message = err instanceof Error ? err.message : '';
  switch (message) {
    case INVALID_CURRENT_PASSWORD:
      return temporary ? p.invalidTemporary : p.invalidCurrent;
    case TEMPORARY_PASSWORD_EXPIRED:
      return p.temporaryExpired;
    // Reaches here only when the retry after a proof was refused again.
    case STEP_UP_REQUIRED:
      return t.account.stepUp.refusedAgain;
    default:
      if (err instanceof ApiError && err.status === 429) return t.auth.errors.tooManyAttempts;
      return message || p.failed;
  }
}
