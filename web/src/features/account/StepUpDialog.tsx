import { useState, type FormEvent } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { reauthWithPassword, verifyTotp } from '../../api/hooks/useAuth.ts';
import { useCachedMe } from '../../api/hooks/useMe.ts';
import { passkeysSupported, reauthWithPasskey } from '../auth/passkeys.ts';
import { useT } from '../../i18n/useT.ts';

/**
 * StepUpDialog re-proves the account's second factor so the action the
 * user just tried can go through. Managing credentials is exactly what an
 * attacker on an unattended, still-signed-in screen would reach for, so
 * the server asks again after five minutes — and this is where the user
 * answers.
 *
 * Both factors work: a code from the authenticator (or a recovery code),
 * and the passkey ceremony, which matters because a passkey-only account
 * has no code to type.
 *
 * An account without a second factor on an instance where it is optional
 * (`password_step_up` on /auth/me) has neither: its password is its
 * step-up, so the dialog asks for that alone.
 */
export function StepUpDialog({ onClose, onProved }: { onClose: () => void; onProved: () => void }) {
  const t = useT();
  const passwordOnly = useCachedMe().data?.password_step_up === true;
  const [code, setCode] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function prove(run: () => Promise<unknown>) {
    setBusy(true);
    setError(null);
    try {
      await run();
      onProved();
    } catch (err) {
      setError(err instanceof Error ? err.message : t.account.stepUp.failed);
    } finally {
      setBusy(false);
    }
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    if (passwordOnly) {
      if (password) void prove(() => reauthWithPassword(password));
      return;
    }
    const entered = code.trim();
    if (!entered) return;
    // A recovery code carries the dashes the authenticator's six digits
    // never do, which is how the field takes both without a second input.
    void prove(() => (entered.includes('-') ? verifyTotp({ recoveryCode: entered }) : verifyTotp({ code: entered })));
  }

  if (passwordOnly) {
    return (
      <Dialog open onClose={onClose} title={t.account.stepUp.title}>
        <form className="flex flex-col gap-3" onSubmit={submit}>
          <p className="text-xs text-muted-foreground">{t.account.stepUp.introPassword}</p>
          <Label htmlFor="step-up-password">{t.account.stepUp.passwordLabel}</Label>
          <Input
            id="step-up-password"
            type="password"
            autoFocus
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
          {error && <p className="text-xs text-[var(--px-crit)]">{error}</p>}
          <div className="flex justify-end gap-2">
            <Button type="button" onClick={onClose}>
              {t.common.cancel}
            </Button>
            <Button type="submit" variant="primary" disabled={busy || !password}>
              {t.account.confirm}
            </Button>
          </div>
        </form>
      </Dialog>
    );
  }

  return (
    <Dialog open onClose={onClose} title={t.account.stepUp.title}>
      <form className="flex flex-col gap-3" onSubmit={submit}>
        <p className="text-xs text-muted-foreground">{t.account.stepUp.intro}</p>
        <Label htmlFor="step-up-code">{t.account.stepUp.codeLabel}</Label>
        <Input
          id="step-up-code"
          autoFocus
          autoComplete="one-time-code"
          className="font-mono"
          value={code}
          onChange={(e) => setCode(e.target.value)}
          placeholder="123456"
        />
        {error && <p className="text-xs text-[var(--px-crit)]">{error}</p>}
        <div className="flex justify-between gap-2">
          {passkeysSupported() ? (
            <Button type="button" disabled={busy} onClick={() => void prove(() => reauthWithPasskey('step_up', t))}>
              {t.auth.usePasskey}
            </Button>
          ) : (
            <span />
          )}
          <div className="flex gap-2">
            <Button type="button" onClick={onClose}>
              {t.common.cancel}
            </Button>
            <Button type="submit" variant="primary" disabled={busy || !code.trim()}>
              {t.account.confirm}
            </Button>
          </div>
        </div>
      </form>
    </Dialog>
  );
}
