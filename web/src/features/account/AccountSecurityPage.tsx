import { useState } from 'react';
import { KeyRound, Trash2 } from 'lucide-react';
import { QRCodeSVG } from 'qrcode.react';
import { Card } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Pill } from '../../ui/badge.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useMe } from '../../api/hooks/useMe.ts';
import { useDeletePasskey, usePasskeys } from '../../api/hooks/usePasskeys.ts';
import { useConfirmTotp, useEnrolTotp } from '../../api/hooks/useTotp.ts';
import { registerPasskey } from '../auth/passkeys.ts';

/**
 * AccountSecurityPage follows design/mockups/build.mjs's ACCOUNT SECURITY
 * artboard's Passkeys, Authenticator app and Recovery codes cards. That
 * mockup also has Password-change and Sessions cards — v0.1's API has no
 * change-password or list-sessions route (see internal/api/auth_handlers.go,
 * internal/api/mfa_handlers.go, internal/api/passkey_handlers.go: nothing
 * beyond login/logout/me, TOTP and passkeys), so those two cards are left
 * out rather than wired to endpoints that do not exist; see
 * task-15a-report.md.
 *
 * Two fixes from that report's fix round 1 live here:
 *
 *  - Critical: the TOTP secret/otpauth_url and the recovery codes used to
 *    live in `mutation.data` with no gcTime override, so the mutation
 *    cache kept them readable (via queryClient.getMutationCache()) for
 *    five minutes after this page unmounted, even though the component's
 *    own state had already been cleared. useEnrolTotp/useConfirmTotp now
 *    set gcTime: 0, and every place that consumes their result also calls
 *    `.reset()` once the secret is no longer needed — as soon as the QR
 *    code is replaced by the confirm step, and as soon as the recovery
 *    codes are explicitly dismissed below (there was previously no
 *    dismissal for them at all).
 *  - Important 4: the header pill used to read `me.data.mfa_complete`,
 *    which says whether this *session* has completed a second factor, not
 *    whether the *account* has one. The status below is derived instead
 *    from whether TOTP is actually confirmed or at least one passkey
 *    exists.
 *
 * The page serves two layouts: the account security page inside the app
 * shell (AccountSecurityLayout) and second-factor enrolment for an account
 * that has none yet (EnrolmentLayout, which passes onEnrolled).
 *
 * onEnrolled, when given, is called once the
 * account has just gained a second factor and the user is done with this
 * page: after they dismiss the recovery codes of a freshly confirmed TOTP
 * secret, or right after a passkey is registered.
 */
export function AccountSecurityPage({ onEnrolled }: { onEnrolled?: () => void | Promise<void> } = {}) {
  const toast = useToast();
  const me = useMe({ enabled: true });
  const passkeys = usePasskeys();
  const deletePasskey = useDeletePasskey();
  const enrolTotp = useEnrolTotp();
  const confirmTotp = useConfirmTotp();

  const [newPasskeyName, setNewPasskeyName] = useState('');
  const [enrolling, setEnrolling] = useState<{ secret: string; otpauth_url: string } | null>(null);
  const [code, setCode] = useState('');
  const [recoveryCodes, setRecoveryCodes] = useState<string[] | null>(null);

  const hasPasskey = (passkeys.data?.passkeys ?? []).length > 0;
  const hasSecondFactor = me.data?.totp_confirmed === true || hasPasskey;

  return (
    <>
      <div className="flex items-center gap-3">
        <h1 className="text-xl font-semibold">Security</h1>
        {hasSecondFactor ? (
          <Pill tone="ok">second factor active</Pill>
        ) : (
          <Pill tone="warn">second factor required</Pill>
        )}
      </div>

      <div className="grid grid-cols-2 gap-4 items-start">
        <Card>
          <div className="flex flex-col p-4 gap-3">
            <h2 className="text-sm font-semibold">Passkeys</h2>
            {(passkeys.data?.passkeys ?? []).map((p) => (
              <div key={p.id} className="flex items-center gap-3 border-b border-border py-2.5 last:border-b-0">
                <KeyRound size={15} className="text-muted-foreground" />
                <div className="flex flex-1 flex-col">
                  <span className="text-xs font-semibold">{p.name}</span>
                  <span className="text-[11px] text-muted-foreground">
                    added {new Date(p.created_at).toLocaleDateString()}
                  </span>
                </div>
                <button
                  type="button"
                  aria-label={`remove ${p.name}`}
                  className="text-destructive"
                  onClick={async () => {
                    try {
                      await deletePasskey.mutateAsync(p.id);
                      toast.show(`Passkey ${p.name} removed`);
                    } catch (err) {
                      toast.show(err instanceof Error ? err.message : 'failed to remove passkey', 'destructive');
                    }
                  }}
                >
                  <Trash2 size={14} />
                </button>
              </div>
            ))}
            {(passkeys.data?.passkeys ?? []).length === 0 && (
              <span className="text-xs text-muted-foreground">No passkeys registered yet.</span>
            )}
            <form
              className="flex items-center gap-2 pt-1"
              onSubmit={async (e) => {
                e.preventDefault();
                if (!newPasskeyName.trim()) return;
                try {
                  await registerPasskey(newPasskeyName.trim());
                  toast.show('Passkey added');
                  setNewPasskeyName('');
                  await passkeys.refetch();
                  await onEnrolled?.();
                } catch (err) {
                  toast.show(err instanceof Error ? err.message : 'failed to add passkey', 'destructive');
                }
              }}
            >
              <Input
                value={newPasskeyName}
                onChange={(e) => setNewPasskeyName(e.target.value)}
                placeholder="e.g. MacBook Pro"
              />
              <Button type="submit" variant="primary">
                <KeyRound size={14} /> Add passkey
              </Button>
            </form>
          </div>
        </Card>

        <Card>
          <div className="flex flex-col gap-4 p-4">
            <h2 className="text-sm font-semibold">Authenticator app</h2>
            {!enrolling && !recoveryCodes && (
              <Button
                variant="primary"
                onClick={async () => {
                  try {
                    const res = await enrolTotp.mutateAsync(undefined);
                    setEnrolling({ secret: res.secret, otpauth_url: res.otpauth_url });
                  } catch (err) {
                    toast.show(err instanceof Error ? err.message : 'failed to start enrolment', 'destructive');
                  }
                }}
              >
                Enrol authenticator app
              </Button>
            )}
            {enrolling && (
              <form
                className="flex flex-col gap-3"
                onSubmit={async (e) => {
                  e.preventDefault();
                  try {
                    const res = await confirmTotp.mutateAsync(code);
                    setRecoveryCodes(res.recovery_codes);
                    setEnrolling(null);
                    // The secret is consumed the moment confirmation
                    // succeeds — clear it from the mutation cache right
                    // away rather than waiting for gcTime (Critical fix).
                    enrolTotp.reset();
                  } catch (err) {
                    toast.show(err instanceof Error ? err.message : 'invalid code', 'destructive');
                  }
                }}
              >
                <p className="text-[11px] text-muted-foreground">
                  Scan with any TOTP app, or enter the secret manually, then confirm with a 6-digit code.
                </p>
                <QRCodeSVG
                  value={enrolling.otpauth_url}
                  size={132}
                  fgColor="#0E1117"
                  bgColor="#F6F4EF"
                  className="self-start rounded-md p-2"
                  style={{ background: 'var(--px-paper)' }}
                />
                <code className="break-all rounded-md border border-border bg-background p-2 font-mono text-[11px]">
                  {enrolling.secret}
                </code>
                <div className="flex items-end gap-2">
                  <Input
                    inputMode="numeric"
                    pattern="[0-9]{6}"
                    placeholder="482913"
                    value={code}
                    onChange={(e) => setCode(e.target.value)}
                  />
                  <Button type="submit" variant="primary">
                    Confirm
                  </Button>
                </div>
              </form>
            )}
            {recoveryCodes && (
              <div className="flex flex-col gap-2">
                <p className="text-[11px] text-muted-foreground">
                  Recovery codes — shown once. Each works one time.
                </p>
                <div className="grid grid-cols-2 gap-1.5 rounded-md border border-border bg-background p-3 font-mono text-xs">
                  {recoveryCodes.map((c) => (
                    <span key={c}>{c}</span>
                  ))}
                </div>
                <Button
                  variant="primary"
                  onClick={async () => {
                    setRecoveryCodes(null);
                    setCode('');
                    // Critical fix: the recovery codes were never
                    // dismissable at all before this, and even clearing
                    // this component's own state does not clear
                    // TanStack Query's mutation cache — reset() does.
                    confirmTotp.reset();
                    await onEnrolled?.();
                  }}
                >
                  I have saved these
                </Button>
              </div>
            )}
          </div>
        </Card>
      </div>
    </>
  );
}
