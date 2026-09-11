import { useState, type FormEvent } from 'react';
import { useNavigate, useSearch } from '@tanstack/react-router';
import { ArrowLeft, KeyRound } from 'lucide-react';
import { PulsixMark } from '../../ui/PulsixMark.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { login, verifyTotp } from '../../api/hooks/useAuth.ts';
import { useRefetchMe } from '../../api/hooks/useMe.ts';
import { beginLogin, submitPassword, submitTotp, type LoginState } from './loginMachine.ts';
import { loginWithPasskey, passkeysSupported } from './passkeys.ts';

/**
 * LoginPage — no dedicated mockup exists for this screen among the 21 in
 * design/mockups/build.mjs (v0.1's Login is new relative to what was
 * mocked up), so it follows the SetupWizard's own split-panel language
 * (brand panel + centered form, same tokens and spacing) for visual
 * consistency rather than inventing an unrelated layout.
 *
 * Two fixes from task-15a-report.md's fix round 1 live here:
 *
 *  - Important 3: afterAuthenticated used to only call invalidateMe(),
 *    which marks the ['me'] query stale but — with no useMe observer
 *    mounted while we're still on /login — does not actually refetch it.
 *    Navigating to / then remounted RootRedirect, which read the still-
 *    stale cached `mfa_complete: false` before its own background
 *    refetch resolved, bouncing a correctly verified session straight
 *    back to the TOTP prompt. refetchMe forces the fetch and is awaited
 *    before navigating, so the cache is already current by the time
 *    RootRedirect reads it.
 *  - Important 5: the TOTP step used to accept only a 6-digit code, with
 *    no way to use a recovery code, no passkey option, and no way back
 *    to the password step — so the recovery codes the app hands out on
 *    enrolment could never be used to log in, and a passkey-only user who
 *    authenticates with a password (a real MFA-pending state — see
 *    auth.AnyEnabled) had no way to complete the session at all. All
 *    three are offered on the TOTP step below.
 */
export function LoginPage() {
  const search = useSearch({ strict: false }) as { step?: string };
  const navigate = useNavigate();
  const toast = useToast();
  const refetchMe = useRefetchMe();

  const [state, setState] = useState<LoginState>(() =>
    search.step === 'totp' ? { step: 'totp' } : beginLogin(),
  );
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [code, setCode] = useState('');
  const [recoveryCode, setRecoveryCode] = useState('');
  const [useRecoveryCode, setUseRecoveryCode] = useState(false);
  const [busy, setBusy] = useState(false);

  async function afterAuthenticated() {
    await refetchMe();
    await navigate({ to: '/' });
  }

  async function handlePasswordSubmit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      const next = await submitPassword(login, email, password);
      setState(next);
      if (next.step === 'done') await afterAuthenticated();
    } catch (err) {
      toast.show(err instanceof Error ? err.message : 'sign in failed', 'destructive');
    } finally {
      setBusy(false);
    }
  }

  async function handleTotpSubmit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      const next = await submitTotp(verifyTotp, useRecoveryCode ? { recoveryCode } : { code });
      setState(next);
      if (next.step === 'done') await afterAuthenticated();
    } catch (err) {
      toast.show(err instanceof Error ? err.message : 'verification failed', 'destructive');
    } finally {
      setBusy(false);
    }
  }

  async function handlePasskeyLogin() {
    setBusy(true);
    try {
      await loginWithPasskey();
      await afterAuthenticated();
    } catch (err) {
      toast.show(err instanceof Error ? err.message : 'passkey sign-in was cancelled', 'destructive');
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4 text-foreground">
      <div className="flex w-full max-w-[380px] flex-col gap-7">
        <div className="flex items-center gap-3">
          <PulsixMark size={32} />
          <span className="font-mono text-2xl font-extrabold tracking-tight">
            pulsi<span className="text-brand">x</span>
          </span>
        </div>

        {state.step !== 'totp' ? (
          <form onSubmit={handlePasswordSubmit} className="flex flex-col gap-4 rounded-xl border border-border bg-card p-5">
            <h1 className="text-lg font-semibold">Sign in</h1>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="email">Email address</Label>
              <Input
                id="email"
                type="email"
                autoComplete="username"
                required
                value={email}
                onChange={(e) => setEmail(e.target.value)}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="password">Password</Label>
              <Input
                id="password"
                type="password"
                autoComplete="current-password"
                required
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            </div>
            {state.error && <p className="text-xs text-destructive">{state.error}</p>}
            <Button type="submit" variant="primary" disabled={busy}>
              Sign in
            </Button>
            {passkeysSupported() && (
              <Button type="button" variant="default" disabled={busy} onClick={handlePasskeyLogin}>
                <KeyRound size={15} /> Use a passkey
              </Button>
            )}
          </form>
        ) : (
          <form onSubmit={handleTotpSubmit} className="flex flex-col gap-4 rounded-xl border border-border bg-card p-5">
            <div className="flex items-center gap-2">
              <button
                type="button"
                aria-label="Back to sign in"
                className="text-muted-foreground"
                onClick={() => {
                  setState(beginLogin());
                  setCode('');
                  setRecoveryCode('');
                  setUseRecoveryCode(false);
                }}
              >
                <ArrowLeft size={16} />
              </button>
              <h1 className="text-lg font-semibold">Enter your second-factor code</h1>
            </div>

            {!useRecoveryCode ? (
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="code">6-digit code</Label>
                <Input
                  id="code"
                  inputMode="numeric"
                  pattern="[0-9]{6}"
                  autoFocus
                  required
                  value={code}
                  onChange={(e) => setCode(e.target.value)}
                />
              </div>
            ) : (
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="recovery-code">Recovery code</Label>
                <Input
                  id="recovery-code"
                  autoFocus
                  required
                  autoCapitalize="off"
                  autoCorrect="off"
                  value={recoveryCode}
                  onChange={(e) => setRecoveryCode(e.target.value)}
                />
              </div>
            )}
            {state.error && <p className="text-xs text-destructive">{state.error}</p>}
            <Button type="submit" variant="primary" disabled={busy}>
              Verify
            </Button>
            <button
              type="button"
              className="text-left text-[11px] text-brand-dim"
              onClick={() => setUseRecoveryCode((v) => !v)}
            >
              {useRecoveryCode ? 'Use a 6-digit code instead' : 'Use a recovery code'}
            </button>
            {passkeysSupported() && (
              <Button type="button" variant="default" disabled={busy} onClick={handlePasskeyLogin}>
                <KeyRound size={15} /> Use a passkey instead
              </Button>
            )}
          </form>
        )}
      </div>
    </div>
  );
}
