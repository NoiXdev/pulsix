import { useState, type FormEvent } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { ApiError } from '../../api/client.ts';
import { renewSession } from '../../api/hooks/useAuth.ts';
import { refetchMe, useCachedMe } from '../../api/hooks/useMe.ts';
import { useT } from '../../i18n/useT.ts';
import { passkeysSupported, reauthWithPasskey } from '../auth/passkeys.ts';
import { postSessionMessage } from '../lock/lockChannel.ts';
import { mmss, useCountdown } from './useCountdown.ts';
import { markRenewed } from '../../lib/sessionEnd.ts';


/**
 * RenewDialog is the warning ExpiryWatcher shows shortly before the session
 * ends (backlog 3.94): a countdown and the way to stay signed in without
 * leaving the page. Renewing needs the account's password and a second
 * factor (a code or a recovery code), or a passkey. "Later" and Escape hand
 * over to RenewBanner: a modal that made the page inert would stop the
 * person from saving what they are working on, which is the point of the
 * warning.
 *
 * An account whose password is its step-up (`password_step_up`: the second
 * factor is optional on the instance and the account has none) renews with
 * the password alone, so the code field is left out.
 */
export function RenewDialog({
  expiresAtMs,
  onRenewed,
  onLater,
}: {
  expiresAtMs: number;
  onRenewed: () => void;
  onLater: () => void;
}) {
  const t = useT();
  const qc = useQueryClient();
  const left = useCountdown(expiresAtMs);
  const passwordOnly = useCachedMe().data?.password_step_up === true;
  const [password, setPassword] = useState('');
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function renew(run: () => Promise<unknown>, failure: string) {
    setBusy(true);
    setError(null);
    try {
      await run();
    } catch (err) {
      setBusy(false);
      if (err instanceof ApiError && err.status === 401) setError(t.session.wrongCredentials);
      else if (err instanceof ApiError && err.status === 429) setError(t.session.tooManyAttempts);
      else setError(failure);
      return;
    }
    markRenewed();
    // The server now holds a new session behind a new cookie. Read it back
    // (its expiry, and the server clock with it), tell the other tabs, close.
    const fresh = await refetchMe(qc).catch(() => null);
    postSessionMessage({ type: 'renewed', expiresAt: fresh?.session_expires_at ?? '' });
    setBusy(false);
    onRenewed();
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    if (passwordOnly) {
      if (password && !busy) void renew(() => renewSession({ password }), t.session.failed);
      return;
    }
    const entered = code.trim();
    if (!password || !entered || busy) return;
    // A recovery code carries dashes an authenticator's digits never do.
    void renew(
      () =>
        entered.includes('-')
          ? renewSession({ password, recoveryCode: entered })
          : renewSession({ password, code: entered }),
      t.session.failed,
    );
  }

  return (
    <Dialog open onClose={onLater} title={t.session.title}>
      <form className="flex flex-col gap-3" onSubmit={submit}>
        <p className="text-xs text-muted-foreground">{t.session.intro}</p>
        <p className="text-xs text-muted-foreground">
          {t.session.remaining}: <span className="font-mono text-base text-foreground">{mmss(left)}</span>
        </p>
        <Label htmlFor="renew-password">{t.session.passwordLabel}</Label>
        <Input
          id="renew-password"
          type="password"
          autoFocus
          autoComplete="current-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
        {!passwordOnly && (
          <>
            <Label htmlFor="renew-code">{t.session.codeLabel}</Label>
            <Input
              id="renew-code"
              autoComplete="one-time-code"
              className="font-mono"
              value={code}
              onChange={(e) => setCode(e.target.value)}
              placeholder="123456"
            />
          </>
        )}
        {error && (
          <p role="alert" className="text-xs text-[var(--px-crit)]">
            {error}
          </p>
        )}
        <div className="flex justify-between gap-2">
          {passkeysSupported() && !passwordOnly ? (
            <Button
              type="button"
              disabled={busy}
              onClick={() => void renew(() => reauthWithPasskey('renew', t), t.session.passkeyFailed)}
            >
              {t.auth.usePasskey}
            </Button>
          ) : (
            <span />
          )}
          <div className="flex gap-2">
            <Button type="button" onClick={onLater}>
              {t.session.later}
            </Button>
            <Button type="submit" variant="primary" disabled={busy || !password || (!passwordOnly && !code.trim())}>
              {busy ? t.session.renewing : t.session.renew}
            </Button>
          </div>
        </div>
      </form>
    </Dialog>
  );
}
