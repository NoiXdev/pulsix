import { useState, type FormEvent } from 'react';
import { useNavigate, useSearch } from '@tanstack/react-router';
import { KeyRound } from 'lucide-react';
import { PulsixMark } from '../../ui/PulsixMark.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { login, verifyTotp } from '../../api/hooks/useAuth.ts';
import { useInvalidateMe } from '../../api/hooks/useMe.ts';
import { beginLogin, submitPassword, submitTotp, type LoginState } from './loginMachine.ts';
import { loginWithPasskey, passkeysSupported } from './passkeys.ts';

/**
 * LoginPage — no dedicated mockup exists for this screen among the 21 in
 * design/mockups/build.mjs (v0.1's Login is new relative to what was
 * mocked up), so it follows the SetupWizard's own split-panel language
 * (brand panel + centered form, same tokens and spacing) for visual
 * consistency rather than inventing an unrelated layout.
 */
export function LoginPage() {
  const search = useSearch({ strict: false }) as { step?: string };
  const navigate = useNavigate();
  const invalidateMe = useInvalidateMe();

  const [state, setState] = useState<LoginState>(() =>
    search.step === 'totp' ? { step: 'totp' } : beginLogin(),
  );
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState(false);

  async function afterAuthenticated() {
    await invalidateMe();
    await navigate({ to: '/' });
  }

  async function handlePasswordSubmit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      const next = await submitPassword(login, email, password);
      setState(next);
      if (next.step === 'done') await afterAuthenticated();
    } finally {
      setBusy(false);
    }
  }

  async function handleTotpSubmit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      const next = await submitTotp(verifyTotp, code);
      setState(next);
      if (next.step === 'done') await afterAuthenticated();
    } finally {
      setBusy(false);
    }
  }

  async function handlePasskeyLogin() {
    setBusy(true);
    try {
      await loginWithPasskey();
      await afterAuthenticated();
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
            pulsi<span className="text-accent">x</span>
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
            <h1 className="text-lg font-semibold">Enter your second-factor code</h1>
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
            {state.error && <p className="text-xs text-destructive">{state.error}</p>}
            <Button type="submit" variant="primary" disabled={busy}>
              Verify
            </Button>
          </form>
        )}
      </div>
    </div>
  );
}
