import { useEffect, useState, type FormEvent } from 'react';
import { Link } from '@tanstack/react-router';
import { AuthCard, AuthShell } from '../../ui/auth-shell.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Field } from '../../ui/field.tsx';
import { useResetPassword } from '../../api/hooks/usePassword.ts';
import { ApiError } from '../../api/client.ts';
import { tokenFromFragment } from '../../lib/fragment.ts';
import { MIN_PASSWORD_LENGTH, newPasswordProblem, type NewPasswordProblem } from '../../lib/password.ts';
import { useT } from '../../i18n/useT.ts';

/** INVALID_OR_EXPIRED_LINK is the server's refusal of a token that is
 * unknown, used, expired or replaced (internal/api/password_reset_handlers.go);
 * it does not say which. */
const INVALID_OR_EXPIRED_LINK = 'invalid_or_expired_link';

type Outcome = 'form' | 'done' | 'invalid';

/**
 * ResetPasswordPage (/reset-password#token=…, public) is where a reset mail
 * leads: it asks for the new password twice and sets it with the link's
 * token.
 *
 * The token rides in the URL fragment, which a browser never sends to a
 * server, so no request log ever records it. The page reads it from
 * window.location.hash once, takes it out of the address bar (out of the
 * history and of a shared screen), and sends it only in the request body.
 *
 * A reset signs nobody in: on success the page sends the person to sign in
 * with the new password and their second factor. A link that is unknown,
 * used or expired gets one answer, with the way to a new one.
 */
export function ResetPasswordPage() {
  const t = useT();
  const reset = useResetPassword();
  const [token] = useState(() => tokenFromFragment(window.location.hash));
  const [outcome, setOutcome] = useState<Outcome>(() => (token ? 'form' : 'invalid'));
  const [next, setNext] = useState('');
  const [repeat, setRepeat] = useState('');
  const [problem, setProblem] = useState<NewPasswordProblem | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!window.location.hash) return;
    window.history.replaceState(window.history.state, '', window.location.pathname + window.location.search);
  }, []);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    const found = newPasswordProblem(next, repeat);
    setProblem(found);
    if (found || !token) return;
    try {
      await reset.mutateAsync({ token, new_password: next });
      setOutcome('done');
    } catch (err) {
      if (err instanceof ApiError && err.status === 400 && err.message === INVALID_OR_EXPIRED_LINK) {
        setOutcome('invalid');
      } else if (err instanceof ApiError && err.status === 422) {
        setProblem('tooShort');
      } else if (err instanceof ApiError && err.status === 429) {
        setError(t.auth.errors.tooManyAttempts);
      } else {
        setError(t.auth.reset.failed);
      }
    } finally {
      reset.reset();
    }
  }

  if (outcome === 'done') {
    return (
      <AuthShell>
        <AuthCard>
          <h1 className="text-lg font-semibold">{t.auth.reset.doneTitle}</h1>
          <p className="text-xs text-muted-foreground">{t.auth.reset.done}</p>
          <Link to="/login" search={{ step: undefined }} className="text-[11px] text-brand-dim">
            {t.auth.reset.goToSignIn}
          </Link>
        </AuthCard>
      </AuthShell>
    );
  }

  if (outcome === 'invalid') {
    return (
      <AuthShell>
        <AuthCard>
          <h1 className="text-lg font-semibold">{t.auth.reset.title}</h1>
          <p className="text-xs text-muted-foreground">{t.auth.reset.invalidLink}</p>
          <Link to="/forgot-password" className="text-[11px] text-brand-dim">
            {t.auth.reset.requestNew}
          </Link>
        </AuthCard>
      </AuthShell>
    );
  }

  return (
    <AuthShell>
      <AuthCard>
        <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
          <h1 className="text-lg font-semibold">{t.auth.reset.title}</h1>
          <Field
            id="new-password"
            label={t.auth.newPassword}
            hint={t.auth.minLengthHint(MIN_PASSWORD_LENGTH)}
            error={problem === 'tooShort' ? t.validation.minChars(MIN_PASSWORD_LENGTH) : null}
          >
            <Input
              id="new-password"
              type="password"
              autoComplete="new-password"
              required
              value={next}
              onChange={(e) => setNext(e.target.value)}
            />
          </Field>
          <Field
            id="repeat-password"
            label={t.auth.repeatNewPassword}
            error={problem === 'mismatch' ? t.validation.passwordsMismatch : null}
          >
            <Input
              id="repeat-password"
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
          <Button type="submit" variant="primary" disabled={reset.isPending}>
            {t.auth.reset.submit}
          </Button>
        </form>
      </AuthCard>
    </AuthShell>
  );
}
