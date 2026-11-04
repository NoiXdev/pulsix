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
import { useConfirmTotp, useEnrolTotp, useRegenerateRecoveryCodes } from '../../api/hooks/useTotp.ts';
import { registerPasskey } from '../auth/passkeys.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { useStepUp } from './stepUp.tsx';
import { LanguageCard } from './LanguageCard.tsx';
import { PasswordCard } from './PasswordCard.tsx';

/**
 * AccountSecurityPage follows design/mockups/build.mjs's ACCOUNT SECURITY
 * artboard's Passkeys, Authenticator app and Recovery codes cards, and its
 * Password card (backlog 7.74, PasswordCard). The mockup's Sessions card is
 * left out: the API has no route to list one's own sessions (a non-goal of
 * 7.74 too).
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
 * The Password and Language cards (backlog 7.74 and 1.41) sit below the
 * factor cards on the account page only.
 *
 * onEnrolled, when given, is called once the
 * account has just gained a second factor and the user is done with this
 * page: after they dismiss the recovery codes of a freshly confirmed TOTP
 * secret, or right after a passkey is registered.
 */
export function AccountSecurityPage({ onEnrolled }: { onEnrolled?: () => void | Promise<void> } = {}) {
  const toast = useToast();
  const t = useT();
  const fmt = useFormat();
  const me = useMe({ enabled: true });
  const passkeys = usePasskeys();
  const deletePasskey = useDeletePasskey();
  const enrolTotp = useEnrolTotp();
  const confirmTotp = useConfirmTotp();

  // Account-security actions need a recently proven factor; this re-asks
  // and repeats the action rather than failing it (backlog 1.28).
  const stepUp = useStepUp();
  const [newPasskeyName, setNewPasskeyName] = useState('');
  const [enrolling, setEnrolling] = useState<{ secret: string; otpauth_url: string } | null>(null);
  const [code, setCode] = useState('');
  const [recoveryCodes, setRecoveryCodes] = useState<string[] | null>(null);
  const regenerate = useRegenerateRecoveryCodes();

  const hasPasskey = (passkeys.data?.passkeys ?? []).length > 0;
  const hasSecondFactor = me.data?.totp_confirmed === true || hasPasskey;

  return (
    <>
      <div className="flex items-center gap-3">
        <h1 className="text-xl font-semibold">{t.account.title}</h1>
        {hasSecondFactor ? (
          <Pill tone="ok">{t.account.secondFactorActive}</Pill>
        ) : (
          <Pill tone="warn">{t.account.secondFactorRequired}</Pill>
        )}
      </div>

      <div className="grid grid-cols-2 gap-4 items-start">
        <Card>
          <div className="flex flex-col p-4 gap-3">
            <h2 className="text-sm font-semibold">{t.account.passkeys.title}</h2>
            {(passkeys.data?.passkeys ?? []).map((p) => (
              <div key={p.id} className="flex items-center gap-3 border-b border-border py-2.5 last:border-b-0">
                <KeyRound size={15} className="text-muted-foreground" />
                <div className="flex flex-1 flex-col">
                  <span className="text-xs font-semibold">{p.name}</span>
                  <span className="text-[11px] text-muted-foreground">
                    {t.account.passkeys.added(fmt.date(p.created_at))}
                  </span>
                </div>
                <button
                  type="button"
                  aria-label={t.account.passkeys.removeAria(p.name)}
                  className="text-destructive"
                  onClick={() =>
                    void stepUp.run(
                      async () => {
                        await deletePasskey.mutateAsync(p.id);
                        toast.show(t.account.passkeys.removed(p.name));
                      },
                      (err) => toast.show(err instanceof Error ? err.message : t.account.passkeys.removeFailed, 'destructive'),
                    )
                  }
                >
                  <Trash2 size={14} />
                </button>
              </div>
            ))}
            {(passkeys.data?.passkeys ?? []).length === 0 && (
              <span className="text-xs text-muted-foreground">{t.account.passkeys.none}</span>
            )}
            <form
              className="flex items-center gap-2 pt-1"
              onSubmit={(e) => {
                e.preventDefault();
                if (!newPasskeyName.trim()) return;
                void stepUp.run(
                  async () => {
                    await registerPasskey(newPasskeyName.trim(), t);
                    toast.show(t.account.passkeys.addedToast);
                    setNewPasskeyName('');
                    await passkeys.refetch();
                    await onEnrolled?.();
                  },
                  (err) => toast.show(err instanceof Error ? err.message : t.account.passkeys.addFailed, 'destructive'),
                );
              }}
            >
              <Input
                value={newPasskeyName}
                onChange={(e) => setNewPasskeyName(e.target.value)}
                placeholder={t.account.passkeys.namePlaceholder}
              />
              <Button type="submit" variant="primary">
                <KeyRound size={14} /> {t.account.passkeys.add}
              </Button>
            </form>
          </div>
        </Card>

        <Card>
          <div className="flex flex-col gap-4 p-4">
            <h2 className="text-sm font-semibold">{t.account.totp.title}</h2>
            {!enrolling && !recoveryCodes && (
              <Button
                variant="primary"
                onClick={() =>
                  void stepUp.run(
                    async () => {
                      const res = await enrolTotp.mutateAsync(undefined);
                      setEnrolling({ secret: res.secret, otpauth_url: res.otpauth_url });
                    },
                    (err) => toast.show(err instanceof Error ? err.message : t.account.totp.enrolFailed, 'destructive'),
                  )
                }
              >
                {t.account.totp.enrol}
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
                    toast.show(err instanceof Error ? err.message : t.auth.errors.invalidCode, 'destructive');
                  }
                }}
              >
                <p className="text-[11px] text-muted-foreground">{t.account.totp.scanHint}</p>
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
                    {t.account.confirm}
                  </Button>
                </div>
              </form>
            )}
            {me.data?.totp_confirmed === true && !recoveryCodes && (
              <div className="flex items-center justify-between gap-3 border-t border-border pt-3">
                <p className="text-[11px] text-muted-foreground">
                  {t.account.recovery.left}{' '}
                  <span data-testid="recovery-codes-remaining" className="font-mono text-foreground">
                    {me.data?.recovery_codes_remaining ?? 0}
                  </span>
                  {' — '}
                  {t.account.recovery.leftHint}
                </p>
                <Button
                  variant="ghost"
                  disabled={regenerate.isPending}
                  onClick={async () => {
                    try {
                      const res = await regenerate.mutateAsync();
                      setRecoveryCodes(res.recovery_codes);
                    } catch {
                      toast.show(t.account.recovery.regenerateFailed, 'destructive');
                    }
                  }}
                >
                  {t.account.recovery.regenerate}
                </Button>
              </div>
            )}
            {recoveryCodes && (
              <div className="flex flex-col gap-2">
                <p className="text-[11px] text-muted-foreground">{t.account.recovery.shownOnce}</p>
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
                    regenerate.reset();
                    await onEnrolled?.();
                  }}
                >
                  {t.account.recovery.saved}
                </Button>
              </div>
            )}
          </div>
        </Card>

        {/* Not during second-factor enrolment (EnrolmentLayout passes
            onEnrolled): that page has one job. */}
        {!onEnrolled && <PasswordCard />}
        {!onEnrolled && <LanguageCard />}
      </div>
      {stepUp.dialog}
    </>
  );
}
