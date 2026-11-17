import { useEffect, useState, type FormEvent } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { ApiError } from '../../api/client.ts';
import { renewSession } from '../../api/hooks/useAuth.ts';
import { refetchMe } from '../../api/hooks/useMe.ts';
import { serverNow } from '../../lib/serverClock.ts';
import { useT } from '../../i18n/useT.ts';
import { passkeysSupported, reauthWithPasskey } from '../auth/passkeys.ts';
import { postSessionMessage } from '../lock/lockChannel.ts';

function secondsLeft(expiresAtMs: number): number {
  return Math.max(0, Math.ceil((expiresAtMs - serverNow()) / 1000));
}

function mmss(total: number): string {
  return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, '0')}`;
}

/**
 * RenewDialog is the warning ExpiryWatcher shows shortly before the session
 * ends (backlog 3.93): a countdown and the way to stay signed in without
 * leaving the page. Renewing needs the account's password and a second
 * factor (a code or a recovery code), or a passkey. It cannot be dismissed:
 * the only ways out are renewing and letting the time run out.
 */
export function RenewDialog({ expiresAtMs, onRenewed }: { expiresAtMs: number; onRenewed: () => void }) {
  const t = useT();
  const qc = useQueryClient();
  const [left, setLeft] = useState(() => secondsLeft(expiresAtMs));
  const [password, setPassword] = useState('');
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const id = setInterval(() => setLeft(secondsLeft(expiresAtMs)), 1000);
    return () => clearInterval(id);
  }, [expiresAtMs]);

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
    // The server now holds a new session behind a new cookie. Read it back
    // (its expiry, and the server clock with it), tell the other tabs, close.
    const fresh = await refetchMe(qc).catch(() => null);
    postSessionMessage({ type: 'renewed', expiresAt: fresh?.session_expires_at ?? '' });
    setBusy(false);
    onRenewed();
  }

  function submit(e: FormEvent) {
    e.preventDefault();
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
    <Dialog open onClose={() => {}} title={t.session.title} dismissible={false}>
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
        <Label htmlFor="renew-code">{t.session.codeLabel}</Label>
        <Input
          id="renew-code"
          autoComplete="one-time-code"
          className="font-mono"
          value={code}
          onChange={(e) => setCode(e.target.value)}
          placeholder="123456"
        />
        {error && (
          <p role="alert" className="text-xs text-[var(--px-crit)]">
            {error}
          </p>
        )}
        <div className="flex justify-between gap-2">
          {passkeysSupported() ? (
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
          <Button type="submit" variant="primary" disabled={busy || !password || !code.trim()}>
            {busy ? t.session.renewing : t.session.renew}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
