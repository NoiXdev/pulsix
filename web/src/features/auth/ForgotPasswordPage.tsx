import { useState, type FormEvent } from 'react';
import { Link } from '@tanstack/react-router';
import { AuthCard, AuthShell } from '../../ui/auth-shell.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Field } from '../../ui/field.tsx';
import { useRequestReset, useResetAvailable } from '../../api/hooks/usePassword.ts';
import { ApiError } from '../../api/client.ts';
import { useT } from '../../i18n/useT.ts';

/**
 * ForgotPasswordPage (/forgot-password, public) asks for an account's
 * e-mail address and sends a reset link to it — when the instance can send
 * mail. Without mail it says so and sends the person to an instance
 * administrator, who can set a temporary password.
 *
 * The answer never tells whether the address has an account: the server
 * answers every address alike, and so does this page. A rate limit is the
 * one other answer; it is about the client and the address, not about an
 * account, so it is said as it is.
 */
export function ForgotPasswordPage() {
  const t = useT();
  const available = useResetAvailable();
  const request = useRequestReset();
  const [email, setEmail] = useState('');
  const [sent, setSent] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    try {
      await request.mutateAsync(email.trim());
      setSent(true);
    } catch (err) {
      setError(err instanceof ApiError && err.status === 429 ? t.auth.errors.tooManyAttempts : t.auth.forgot.failed);
    } finally {
      request.reset();
    }
  }

  return (
    <AuthShell>
      <AuthCard>
        <h1 className="text-lg font-semibold">{t.auth.forgot.title}</h1>
        {available.isPending ? (
          <p className="text-xs text-muted-foreground">{t.common.loading}</p>
        ) : available.isError ? (
          <p className="text-xs text-muted-foreground">{t.auth.forgot.checkFailed}</p>
        ) : !available.data.available ? (
          <p className="text-xs text-muted-foreground">{t.auth.forgot.unavailable}</p>
        ) : sent ? (
          <p className="text-xs text-muted-foreground">{t.auth.forgot.sent}</p>
        ) : (
          <form onSubmit={submit} className="flex flex-col gap-4">
            <p className="text-xs text-muted-foreground">{t.auth.forgot.intro}</p>
            <Field id="email" label={t.auth.emailAddress}>
              <Input
                id="email"
                type="email"
                autoComplete="username"
                required
                value={email}
                onChange={(e) => setEmail(e.target.value)}
              />
            </Field>
            {error && (
              <p role="alert" className="text-xs text-destructive">
                {error}
              </p>
            )}
            <Button type="submit" variant="primary" disabled={request.isPending}>
              {t.auth.forgot.submit}
            </Button>
          </form>
        )}
        <Link to="/login" search={{ step: undefined }} className="text-[11px] text-brand-dim">
          {t.auth.forgot.backToSignIn}
        </Link>
      </AuthCard>
    </AuthShell>
  );
}
