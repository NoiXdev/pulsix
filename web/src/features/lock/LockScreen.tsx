import { useEffect, useRef, useState, type FormEvent } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { useQueryClient } from '@tanstack/react-query';
import { Lock } from 'lucide-react';
import { AuthCard, AuthShell } from '../../ui/auth-shell.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { PinInput } from '../../ui/pin-input.tsx';
import { ApiError } from '../../api/client.ts';
import { attemptsLeft, useUnlockSession } from '../../api/hooks/useScreenLock.ts';
import { logout } from '../../api/hooks/useAuth.ts';
import { meQueryKey, refetchMe } from '../../api/hooks/useMe.ts';
import { resetSessionCache } from '../../lib/session.ts';
import { loginSearchAfter401 } from '../../lib/sessionEnd.ts';
import { useT } from '../../i18n/useT.ts';
import { useShake } from './useShake.ts';
import { postSessionMessage } from './lockChannel.ts';
import { useLockState } from './lockState.ts';

/** EXHAUSTED_DETAIL is the problem detail the unlock route answers with
 * when the fifth wrong PIN ended the session
 * (internal/api/screenlock_handlers.go). */
const EXHAUSTED_DETAIL = 'too many wrong pins';

/**
 * LockScreen is rendered by routes/RootLayout.tsx instead of the routed
 * page while the session is locked, so the page's components, and the data
 * they showed, are not in the DOM. Unlocking needs the PIN; the fifth
 * wrong one ends the session on the server (401 "too many wrong pins"),
 * which sends the person to the sign-in page with a notice. Any other 401
 * (the session ended meanwhile) goes there with the ordinary notice.
 */
export function LockScreen() {
  const t = useT();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { setLocked } = useLockState();
  const unlock = useUnlockSession();
  // A PIN from before the 4-digit rule can be up to 8 digits; it cannot be
  // typed into four boxes, so those accounts keep the single field, and so
  // does a lock that cannot tell (no cached /auth/me yet, or one without the
  // flag): the single field takes a 4-digit PIN too. Decided once, from the
  // cache RootLayout's useMe filled and without subscribing, so a background
  // refetch does not swap the field under the person's fingers.
  const [flag] = useState(() => qc.getQueryData<{ lock_pin_legacy?: boolean }>(meQueryKey)?.lock_pin_legacy);
  const legacy = flag !== false;
  const [pin, setPin] = useState('');
  const [shake, playShake] = useShake();
  const [wrong, setWrong] = useState(false);
  const pinRef = useRef<HTMLInputElement>(null);
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

  async function attempt(value: string) {
    if (!value || unlock.isPending) return;
    setError(null);
    setWrong(false);
    try {
      await unlock.mutateAsync(value);
    } catch (err) {
      setPin('');
      // Drop the attempt, PIN included, from the mutation cache (gcTime 0
      // removes it once nothing observes it; reset stops observing it).
      unlock.reset();
      if (err instanceof ApiError && err.status === 409) {
        // Not locked after all (another tab unlocked, or signed in again):
        // the server is the authority, so follow it.
        await refetchMe(qc).catch(() => undefined);
        setLocked(false);
        void qc.invalidateQueries();
        return;
      }
      if (err instanceof ApiError && err.status === 401) {
        // Only the fifth wrong PIN is "too many wrong PINs"; any other 401
        // means the session was already gone (its 12-hour end, a sign-out
        // in another tab, an administrator ending it).
        await leaveToLogin(
          err.message === EXHAUSTED_DETAIL ? { step: undefined, notice: 'lock_ended' } : loginSearchAfter401(qc),
        );
        return;
      }
      const left = attemptsLeft(err);
      if (err instanceof ApiError && err.status === 422) {
        setError(left === undefined ? t.lock.wrongPin : `${t.lock.wrongPin} ${t.lock.attemptsLeft(left)}`);
        setWrong(true);
        playShake();
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

  // The boxes are disabled while the request runs; once they are enabled
  // again after a failure, the cursor goes back into them.
  useEffect(() => {
    if (error && !unlock.isPending && !legacy) pinRef.current?.focus();
  }, [error, unlock.isPending, legacy]);

  function submit(e: FormEvent) {
    e.preventDefault();
    // Enter on a partial PIN must not post: it would burn an attempt.
    if (!legacy && pin.length !== 4) return;
    void attempt(pin);
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
            {legacy ? (
              <>
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
                  aria-describedby={flag === true ? 'lock-pin-legacy' : undefined}
                />
                {flag === true && (
                  <p id="lock-pin-legacy" className="text-xs text-muted-foreground">
                    {t.lock.legacyNote}
                  </p>
                )}
              </>
            ) : (
              <PinInput
                id="lock-pin"
                value={pin}
                onChange={setPin}
                onComplete={(digits) => void attempt(digits)}
                autoFocus
                disabled={unlock.isPending}
                invalid={wrong}
                shake={shake}
                inputRef={pinRef}
                noManagers
              />
            )}
          </div>
          {error && (
            <p role="alert" className="text-xs text-destructive">
              {error}
            </p>
          )}
          <div className="flex items-center gap-2">
            {legacy && (
              <Button type="submit" disabled={!pin || unlock.isPending}>
                {unlock.isPending ? t.lock.unlocking : t.lock.unlock}
              </Button>
            )}
            <Button type="button" variant="ghost" disabled={signingOut} onClick={() => void signOut()}>
              {t.lock.signOut}
            </Button>
          </div>
        </form>
      </AuthCard>
    </AuthShell>
  );
}
