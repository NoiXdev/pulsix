import { useState } from 'react';
import { Link } from '@tanstack/react-router';
import { Copy } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { buttonClass } from '../../ui/buttonStyles.ts';
import { Dialog } from '../../ui/dialog.tsx';
import { DisabledHint } from '../../ui/detail-layout.tsx';
import { useResetAvailable } from '../../api/hooks/usePassword.ts';
import type { AdminUserDetail } from '../../api/types.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import type { Messages } from '../../i18n/types.ts';
import type { ActionKind } from './useAccountActionCalls.ts';
import { useAccountActionRunner, type Revealed } from './useAccountActionRunner.ts';
import { availableActions } from './accountState.ts';

const DESTRUCTIVE: ReadonlySet<ActionKind> = new Set(['resetFactor', 'deactivate']);

/**
 * AccountActions is an account's Actions section body, for instance admins:
 * each action is confirmed in a dialog first, asks for a fresh second
 * factor when the server wants one (and then carries on), and reports how
 * the notice mail to the person went. On the admin's own account it offers
 * nothing and points to Account security instead. Shared by the account
 * page and the member page.
 *
 * The request and its answer belong to this component
 * (useAccountActionRunner), not to the confirm dialog, so a dialog the
 * browser closed mid-request loses nothing.
 *
 * Callers key it by the account (`key={user.id}`): the router keeps a page
 * mounted when only the account in its address changes, and a temporary
 * password revealed for one account must not stay open beside another.
 */
export function AccountActions({ user, selfId }: { user: AdminUserDetail; selfId: string }) {
  const t = useT();
  const resetAvailable = useResetAvailable();
  const runner = useAccountActionRunner(user);
  const a = t.users.actions;
  const openKind = runner.open;

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
          const blocked = kind === 'resetLink' ? resetLinkBlocked(t, resetAvailable) : null;
          return (
            <div key={kind} className="flex items-center gap-4 border-b border-border px-4 py-3 last:border-b-0">
              <span className="min-w-0 flex-1 text-xs text-muted-foreground">
                {blocked ?? a[kind].description}
              </span>
              <Button
                type="button"
                variant={DESTRUCTIVE.has(kind) ? 'destructive' : 'default'}
                disabled={blocked !== null || runner.running !== null}
                aria-busy={runner.running === kind}
                onClick={() => runner.setOpen(kind)}
              >
                {a[kind].label}
              </Button>
            </div>
          );
        })}
      </div>
      {openKind && (
        <ConfirmDialog
          key={openKind}
          kind={openKind}
          email={user.email}
          busy={runner.running === openKind}
          error={runner.error}
          onConfirm={() => runner.confirm(openKind)}
          onClose={() => runner.setOpen(null)}
        />
      )}
      {runner.revealed && <TemporaryPasswordReveal revealed={runner.revealed} onClose={runner.closeReveal} />}
      {runner.stepUpDialog}
    </>
  );
}

/** Why the reset link cannot be sent, or null when it can: it needs
 * outbound mail, so it stays off until the server says mail is there. */
function resetLinkBlocked(t: Messages, available: { data?: { available: boolean }; isError: boolean }): string | null {
  const a = t.users.actions.resetLink;
  if (available.data?.available) return null;
  if (available.data) return a.unavailable;
  return available.isError ? a.checkFailed : a.checking;
}

/** ConfirmDialog says what an action will do and starts it. It holds no
 * state of the request: closing it mid-request loses nothing. */
function ConfirmDialog({
  kind,
  email,
  busy,
  error,
  onConfirm,
  onClose,
}: {
  kind: ActionKind;
  email: string;
  busy: boolean;
  error: string | null;
  onConfirm: () => void;
  onClose: () => void;
}) {
  const t = useT();
  const a = t.users.actions;
  const text = a[kind];
  return (
    <Dialog open onClose={onClose} title={text.label} dismissible={!busy}>
      <div className="flex flex-col gap-4">
        <p className="text-xs text-muted-foreground">{kind === 'resetLink' ? a.resetLink.body(email) : a[kind].body}</p>
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
            onClick={onConfirm}
          >
            {busy ? a.working : text.confirm}
          </Button>
        </div>
      </div>
    </Dialog>
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
        <p className="text-xs">{a.revealAccount(revealed.email)}</p>
        <p className="text-xs text-muted-foreground">{a.revealNote}</p>
        <code
          aria-label={a.passwordLabel}
          className="rounded-md border border-border bg-background p-2 font-mono text-[13px] break-all"
        >
          {revealed.password}
        </code>
        <p className="text-xs text-muted-foreground">{a.expires(fmt.dateTime(revealed.expiresAt))}</p>
        <p
          data-tone={revealed.notice === 'failed' ? 'warn' : 'default'}
          className={revealed.notice === 'failed' ? 'text-xs text-[var(--px-warn)]' : 'text-xs text-muted-foreground'}
        >
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
