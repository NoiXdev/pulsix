import { useState, type FormEvent } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { useQueryClient } from '@tanstack/react-query';
import { Lock } from 'lucide-react';
import { AuthCard, AuthShell } from '../../ui/auth-shell.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { ApiError } from '../../api/client.ts';
import { attemptsLeft, useUnlockSession } from '../../api/hooks/useScreenLock.ts';
import { logout } from '../../api/hooks/useAuth.ts';
import { refetchMe } from '../../api/hooks/useMe.ts';
import { resetSessionCache } from '../../lib/session.ts';
import { useT } from '../../i18n/useT.ts';
import { postSessionMessage } from './lockChannel.ts';
import { useLockState } from './lockState.ts';

/**
 * LockScreen is rendered by routes/RootLayout.tsx instead of the routed
 * page while the session is locked, so the page's components, and the data
 * they showed, are not in the DOM. Unlocking needs the PIN; the fifth
 * wrong one ends the session on the server (401), which sends the person
 * to the sign-in page with a notice.
 */
export function LockScreen() {
  const t = useT();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { setLocked } = useLockState();
  const unlock = useUnlockSession();
  const [pin, setPin] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [signingOut, setSigningOut] = useState(false);

  async function leaveToLogin(search: { step: undefined; notice?: string }) {
    // Unlock first, so the sign-in page is not drawn under the lock screen,
    // then go, then drop the cache (navigate first, reset after: see
    // lib/session.ts).
    setLocked(false);
    await navigate({ to: '/login', search });
    resetSessionCache(qc);
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!pin || unlock.isPending) return;
    setError(null);
    try {
      await unlock.mutateAsync(pin);
    } catch (err) {
      setPin('');
      if (err instanceof ApiError && err.status === 401) {
        await leaveToLogin({ step: undefined, notice: 'lock_ended' });
        return;
      }
      const left = attemptsLeft(err);
      if (err instanceof ApiError && err.status === 422) {
        setError(left === undefined ? t.lock.wrongPin : `${t.lock.wrongPin} ${t.lock.attemptsLeft(left)}`);
      } else {
        setError(t.lock.failed);
      }
      return;
    }
    postSessionMessage({ type: 'unlocked' });
    // The server answered unlocked; make /auth/me say so before the page
    // comes back, then let every query refetch what the lock dropped.
    await refetchMe(qc).catch(() => undefined);
    setLocked(false);
    void qc.invalidateQueries();
  }

  async function signOut() {
    setSigningOut(true);
    try {
      await logout();
    } catch {
      // The session may be gone already; either way the person leaves.
    }
    await leaveToLogin({ step: undefined });
  }

  return (
    <AuthShell>
      <AuthCard>
        <form onSubmit={submit} className="flex flex-col gap-4">
          <h1 className="flex items-center gap-2 text-lg font-semibold">
            <Lock size={18} aria-hidden />
            {t.lock.title}
          </h1>
          <p className="text-xs text-muted-foreground">{t.lock.intro}</p>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="lock-pin">{t.lock.pinLabel}</Label>
            <Input
              id="lock-pin"
              type="password"
              inputMode="numeric"
              autoComplete="off"
              autoFocus
              maxLength={8}
              className="font-mono"
              value={pin}
              onChange={(e) => setPin(e.target.value)}
            />
          </div>
          {error && (
            <p role="alert" className="text-xs text-destructive">
              {error}
            </p>
          )}
          <div className="flex items-center gap-2">
            <Button type="submit" disabled={!pin || unlock.isPending}>
              {unlock.isPending ? t.lock.unlocking : t.lock.unlock}
            </Button>
            <Button type="button" variant="ghost" disabled={signingOut} onClick={() => void signOut()}>
              {t.lock.signOut}
            </Button>
          </div>
        </form>
      </AuthCard>
    </AuthShell>
  );
}
