import { useState } from 'react';
import { StepUpDialog } from './StepUpDialog.tsx';

/** STEP_UP_REQUIRED is what the server answers with (403) when an
 * account-security action needs a recently proven second factor. It is a
 * contract, not prose: internal/api/auth_handlers.go sends exactly this. */
export const STEP_UP_REQUIRED = 'step_up_required';

/** isStepUpRequired recognizes that refusal in whatever an API call threw,
 * so a caller can re-prompt instead of showing the raw message. */
export function isStepUpRequired(err: unknown): boolean {
  return err instanceof Error && err.message.includes(STEP_UP_REQUIRED);
}

/**
 * useStepUp runs an account-security action and, when the server asks for
 * a fresh second factor, re-prompts and runs it again afterwards — so the
 * user's actual intent survives the interruption instead of turning into
 * an error they have to understand and repeat themselves.
 *
 * Any other failure is handed back to the caller's own error handling.
 */
export function useStepUp() {
  const [pending, setPending] = useState<(() => Promise<void>) | null>(null);

  async function run(action: () => Promise<void>, onError: (err: unknown) => void) {
    try {
      await action();
    } catch (err) {
      if (isStepUpRequired(err)) {
        // Stored as a value, not called: setState treats a function
        // argument as an updater.
        setPending(() => action);
        return;
      }
      onError(err);
    }
  }

  const dialog = pending ? (
    <StepUpDialog
      onClose={() => setPending(null)}
      onProved={async () => {
        const retry = pending;
        setPending(null);
        await retry();
      }}
    />
  ) : null;

  return { run, dialog };
}
