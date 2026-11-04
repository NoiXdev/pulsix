import { useState } from 'react';
import { Link } from '@tanstack/react-router';
import { Copy } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { buttonClass } from '../../ui/buttonStyles.ts';
import { Dialog } from '../../ui/dialog.tsx';
import { DisabledHint } from '../../ui/detail-layout.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useResetAvailable } from '../../api/hooks/usePassword.ts';
import type { AccountNotice, AdminUserDetail } from '../../api/types.ts';
import { isStepUpRequired, useStepUp } from '../account/stepUp.tsx';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import type { Messages } from '../../i18n/types.ts';
import { useAccountActionCalls, type ActionKind } from './useAccountActionCalls.ts';
import { availableActions } from './accountState.ts';

const DESTRUCTIVE: ReadonlySet<ActionKind> = new Set(['resetFactor', 'deactivate']);

/**
 * AccountActions is an account's Actions section body, for instance admins:
 * each action is confirmed in a dialog first, asks for a fresh second
 * factor when the server wants one (and then carries on), and reports how
 * the notice mail to the person went. On the admin's own account it offers
 * nothing and points to Account security instead. Shared by the account
 * page and the member page.
 */
export function AccountActions({ user, selfId }: { user: AdminUserDetail; selfId: string }) {
  const t = useT();
  const resetAvailable = useResetAvailable();
  const [open, setOpen] = useState<ActionKind | null>(null);
  const a = t.users.actions;

  if (user.id === selfId) {
    return (
      <DisabledHint
        title={a.ownTitle}
        body={a.ownBody}
        action={
          <Link to="/account/security" className={buttonClass()}>
            {a.ownLink}
          </Link>
        }
      />
    );
  }

  return (
    <>
      <div className="flex flex-col rounded-[10px] border border-border bg-card">
        {availableActions(user.state).map((kind) => {
          const unavailable = kind === 'resetLink' && resetAvailable.data?.available === false;
          return (
            <div key={kind} className="flex items-center gap-4 border-b border-border px-4 py-3 last:border-b-0">
              <span className="min-w-0 flex-1 text-xs text-muted-foreground">
                {unavailable ? a.resetLink.unavailable : a[kind].description}
              </span>
              <Button
                type="button"
                variant={DESTRUCTIVE.has(kind) ? 'destructive' : 'default'}
                disabled={unavailable}
                onClick={() => setOpen(kind)}
              >
                {a[kind].label}
              </Button>
            </div>
          );
        })}
      </div>
      {open && <ActionDialog key={open} kind={open} user={user} onClose={() => setOpen(null)} />}
    </>
  );
}

interface Revealed {
  password: string;
  expiresAt: string;
  notice: AccountNotice;
}

/**
 * ActionDialog confirms one action and runs it. For the temporary password
 * it then turns into the one-time reveal: the password lives in this
 * dialog's state and nowhere else, and is gone when the dialog closes
 * (AccountActions unmounts it).
 */
function ActionDialog({ kind, user, onClose }: { kind: ActionKind; user: AdminUserDetail; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const stepUp = useStepUp();
  const calls = useAccountActionCalls(user.id);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [revealed, setRevealed] = useState<Revealed | null>(null);
  const a = t.users.actions;
  const text = a[kind];

  // The action handles its own failures, except the step-up refusal, which
  // useStepUp turns into the second-factor prompt and a retry. A retry
  // runs outside run()'s error handling, so nothing may escape it.
  async function action() {
    setBusy(true);
    setError(null);
    try {
      const res = await calls[kind]();
      if (res.temporary) {
        const { password, expires_at, notice } = res.temporary;
        setRevealed({ password, expiresAt: expires_at, notice });
        return;
      }
      toast.show(successMessage(t, kind, user.email, res.notice), res.notice === 'failed' ? 'warn' : 'default');
      onClose();
    } catch (err) {
      if (isStepUpRequired(err)) throw err;
      setError(describeError(t, err));
    } finally {
      setBusy(false);
    }
  }

  if (revealed) {
    return <TemporaryPasswordReveal revealed={revealed} onClose={onClose} />;
  }

  return (
    <>
      <Dialog open onClose={onClose} title={text.label}>
        <div className="flex flex-col gap-4">
          <p className="text-xs text-muted-foreground">{kind === 'resetLink' ? a.resetLink.body(user.email) : a[kind].body}</p>
          {kind === 'resetFactor' && (
            <p className="rounded-md border border-[var(--px-warn)] p-3 text-xs text-[var(--px-warn)]">{a.resetFactor.warning}</p>
          )}
          {busy && kind === 'tempPassword' && <p className="text-xs text-muted-foreground">{a.tempPassword.busy}</p>}
          {error && (
            <p role="alert" className="text-xs text-destructive">
              {error}
            </p>
          )}
          <div className="flex justify-end gap-2">
            <Button type="button" disabled={busy} onClick={onClose}>
              {a.cancel}
            </Button>
            <Button
              type="button"
              variant={DESTRUCTIVE.has(kind) ? 'destructive' : 'primary'}
              disabled={busy}
              aria-busy={busy}
              onClick={() => void stepUp.run(action, (err) => setError(describeError(t, err)))}
            >
              {busy ? a.working : text.confirm}
            </Button>
          </div>
        </div>
      </Dialog>
      {stepUp.dialog}
    </>
  );
}

function TemporaryPasswordReveal({ revealed, onClose }: { revealed: Revealed; onClose: () => void }) {
  const t = useT();
  const fmt = useFormat();
  const a = t.users.actions.tempPassword;
  const [copy, setCopy] = useState<'idle' | 'copied' | 'failed'>('idle');
  return (
    <Dialog open onClose={onClose} title={a.revealTitle}>
      <div className="flex flex-col gap-4">
        <p className="text-xs text-muted-foreground">{a.revealNote}</p>
        <code
          aria-label={a.passwordLabel}
          className="rounded-md border border-border bg-background p-2 font-mono text-[13px] break-all"
        >
          {revealed.password}
        </code>
        <p className="text-xs text-muted-foreground">{a.expires(fmt.dateTime(revealed.expiresAt))}</p>
        <p className={revealed.notice === 'failed' ? 'text-xs text-[var(--px-warn)]' : 'text-xs text-muted-foreground'}>
          {t.users.actions.notice[revealed.notice]}
        </p>
        {copy === 'failed' && (
          <p role="alert" className="text-xs text-destructive">
            {a.copyFailed}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button
            type="button"
            onClick={async () => {
              try {
                await navigator.clipboard.writeText(revealed.password);
                setCopy('copied');
              } catch {
                setCopy('failed');
              }
            }}
          >
            <Copy size={14} /> {copy === 'copied' ? a.copied : a.copy}
          </Button>
          <Button type="button" variant="primary" onClick={onClose}>
            {a.finish}
          </Button>
        </div>
      </div>
    </Dialog>
  );
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
 * anything else is the server's own message. The last-admin detail is
 * internal/tenancy's ErrLastInstanceAdmin text. */
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
    case 'the instance needs at least one active instance administrator':
      return e.lastInstanceAdmin;
    case 'instance admin required':
      return e.instanceAdminRequired;
    default:
      return message || e.failed;
  }
}
