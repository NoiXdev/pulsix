import { useState } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { LogOut } from 'lucide-react';
import { BarePage } from '../../ui/bare-page.tsx';
import { Button } from '../../ui/button.tsx';
import { useSetLockPin, useSkipLockPinPrompt } from '../../api/hooks/useScreenLock.ts';
import { useSignOut } from '../../lib/session.ts';
import { useT } from '../../i18n/useT.ts';
import { PinForm } from './PinForm.tsx';

/**
 * LockPinPromptPage is /account/lock-pin: the one-time offer, right after
 * signing in, to set the PIN that lets the session be locked. Both ways
 * out (Set PIN, Skip) continue to the root redirect; the server stops
 * offering it after either. Like enrolment it renders without the app
 * shell, with signing out as the only other action.
 */
export function LockPinPromptPage() {
  const t = useT();
  const navigate = useNavigate();
  const signOut = useSignOut();
  const setPin = useSetLockPin();
  const skip = useSkipLockPinPrompt();
  const [skipError, setSkipError] = useState(false);

  return (
    <BarePage
      action={
        <Button type="button" onClick={() => void signOut()}>
          <LogOut size={14} /> {t.routes.enrolment.signOut}
        </Button>
      }
    >
      <div className="flex max-w-xl flex-col gap-3">
        <h1 className="text-xl font-semibold">{t.lock.prompt.title}</h1>
        <p className="text-muted-foreground">{t.lock.prompt.intro}</p>
        <PinForm
          busy={skip.isPending}
          onSubmit={async (pin) => {
            await setPin.mutateAsync(pin);
            await navigate({ to: '/' });
          }}
          extra={
            <Button
              type="button"
              variant="ghost"
              disabled={skip.isPending || setPin.isPending}
              onClick={async () => {
                setSkipError(false);
                try {
                  await skip.mutateAsync();
                } catch {
                  setSkipError(true);
                  return;
                }
                await navigate({ to: '/' });
              }}
            >
              {t.lock.prompt.skip}
            </Button>
          }
        />
        {skipError && (
          <p role="alert" className="text-xs text-destructive">
            {t.lock.prompt.skipFailed}
          </p>
        )}
      </div>
    </BarePage>
  );
}
