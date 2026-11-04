import { useRef, useState } from 'react';
import { useToast } from '../../ui/toastContext.ts';
import type { AccountNotice, AdminUserDetail } from '../../api/types.ts';
import { isStepUpRequired, useStepUp } from '../account/stepUp.tsx';
import { useT } from '../../i18n/useT.ts';
import type { Messages } from '../../i18n/types.ts';
import { useAccountActionCalls, type ActionKind } from './useAccountActionCalls.ts';

/** A temporary password, held for its one-time reveal. */
export interface Revealed {
  password: string;
  expiresAt: string;
  notice: AccountNotice;
}

/**
 * useAccountActionRunner owns an account action from confirmation to
 * answer, in the component that outlives the confirm dialog (AccountActions).
 *
 * The dialog asks the platform to stay open while a request runs, but the
 * platform does not promise it: a browser's close watcher lets a repeated
 * Escape close a <dialog> although its cancel is prevented. So the request,
 * and what it answers, never depend on the dialog still being there:
 * - a temporary password opens the reveal from this state either way;
 * - a refusal shows in the dialog when it is still open, and as a toast
 *   when it is not;
 * - while a request runs no other action starts.
 *
 * The password lives in this component state only — never in a query or
 * mutation cache — and is dropped when the reveal closes.
 */
export function useAccountActionRunner(user: AdminUserDetail) {
  const t = useT();
  const toast = useToast();
  const stepUp = useStepUp();
  const calls = useAccountActionCalls(user.id);
  const [open, setOpenState] = useState<ActionKind | null>(null);
  // The open dialog as the async action sees it after its await, when the
  // state captured at the start is stale.
  const openRef = useRef<ActionKind | null>(null);
  const [running, setRunning] = useState<ActionKind | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [revealed, setRevealed] = useState<Revealed | null>(null);

  function setOpen(kind: ActionKind | null) {
    openRef.current = kind;
    setOpenState(kind);
    setError(null);
  }

  function refuse(kind: ActionKind, err: unknown) {
    const message = describeError(t, err);
    if (openRef.current === kind) setError(message);
    else toast.show(message, 'destructive');
  }

  // The action handles its own failures, except the step-up refusal, which
  // useStepUp turns into the second-factor prompt and a retry. A retry
  // runs outside run()'s error handling, so nothing else may escape it.
  async function perform(kind: ActionKind) {
    setRunning(kind);
    setError(null);
    try {
      const res = await calls[kind]();
      if (res.temporary) {
        const { password, expires_at, notice } = res.temporary;
        setRevealed({ password, expiresAt: expires_at, notice });
      } else {
        toast.show(successMessage(t, kind, user.email, res.notice), res.notice === 'failed' ? 'warn' : 'default');
      }
      if (openRef.current === kind) setOpen(null);
    } catch (err) {
      if (isStepUpRequired(err)) throw err;
      refuse(kind, err);
    } finally {
      setRunning(null);
    }
  }

  return {
    /** The action whose confirm dialog is open. */
    open,
    setOpen,
    /** The action whose request is in flight. */
    running,
    /** A refusal for the open dialog. */
    error,
    revealed,
    closeReveal: () => setRevealed(null),
    confirm: (kind: ActionKind) => void stepUp.run(() => perform(kind), (err) => refuse(kind, err)),
    stepUpDialog: stepUp.dialog,
  };
}

/** The toast after an action: what happened, and how the mail to the person
 * went. A failed mail is reported, but the action itself stands. */
function successMessage(t: Messages, kind: ActionKind, email: string, notice: AccountNotice | undefined): string {
  const a = t.users.actions;
  if (kind === 'resetLink') {
    switch (notice) {
      case 'failed':
        return a.resetLink.failed;
      case 'skipped':
        return a.resetLink.skipped;
      case 'not_configured':
        return a.resetLink.notConfigured;
      default:
        return a.resetLink.sent(email);
    }
  }
  const done = kind === 'tempPassword' ? '' : a[kind].done;
  return notice ? `${done} ${a.notice[notice]}`.trim() : done;
}

/** The refusals the account routes answer with, in the reader's language;
 * anything else is the server's own message. `instance admin required`
 * is prose, not a code, and only a fallback: the pages are not offered to
 * anyone else. */
function describeError(t: Messages, err: unknown): string {
  const e = t.users.actions.errors;
  const message = err instanceof Error ? err.message : '';
  switch (message) {
    case 'not_on_own_account':
      return e.notOnOwnAccount;
    case 'invite_pending':
      return e.invitePending;
    case 'mail_not_configured':
      return e.mailNotConfigured;
    case 'last_instance_admin':
      return e.lastInstanceAdmin;
    case 'instance admin required':
      return e.instanceAdminRequired;
    default:
      return message || e.failed;
  }
}
