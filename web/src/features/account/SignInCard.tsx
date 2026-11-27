import { useState } from 'react';
import { KeyRound, Trash2 } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { Pill } from '../../ui/badge.tsx';
import { SettingsCard, SettingsRow } from '../../ui/settings.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { Dialog } from '../../ui/dialog.tsx';
import { useMe } from '../../api/hooks/useMe.ts';
import { useDeletePasskey, usePasskeys } from '../../api/hooks/usePasskeys.ts';
import { useConfirmTotp, useEnrolTotp, useRegenerateRecoveryCodes } from '../../api/hooks/useTotp.ts';
import { registerPasskey } from '../auth/passkeys.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { useStepUp } from './stepUp.tsx';
import { PasskeyDialog } from './PasskeyDialog.tsx';
import { PasswordDialog } from './PasswordDialog.tsx';
import { TotpDialog, type TotpEnrolment } from './TotpDialog.tsx';

/**
 * SignInCard is Account security's "Sign-in" card: the password, the
 * authenticator app, the recovery codes and the passkeys, each row opening
 * its flow in a dialog.
 *
 * Two fixes from backlog 7.74's fix round 1 live here:
 *
 *  - Critical: the TOTP secret/otpauth_url and the recovery codes used to
 *    live in `mutation.data` with no gcTime override, so the mutation
 *    cache kept them readable for five minutes after the page unmounted.
 *    useEnrolTotp/useConfirmTotp set gcTime: 0, and every place that
 *    consumes their result also calls `.reset()` once the secret is no
 *    longer needed (when the QR code is replaced by the recovery codes,
 *    and when those are dismissed).
 *  - Important 4: the badge is derived from whether TOTP is confirmed or
 *    a passkey exists (the account), never from `mfa_complete` (this
 *    session).
 *
 * `onEnrolled` (second-factor enrolment, EnrolmentLayout) is called once
 * the account has just gained a second factor and the person is done:
 * after the recovery codes of a freshly confirmed secret are dismissed, or
 * right after a passkey is registered. In that mode the Password row is
 * left out: that page has one job.
 */
export function SignInCard({ onEnrolled }: { onEnrolled?: () => void | Promise<void> }) {
  const t = useT();
  const fmt = useFormat();
  const toast = useToast();
  const me = useMe({ enabled: true });
  const passkeys = usePasskeys();
  const deletePasskey = useDeletePasskey();
  const enrolTotp = useEnrolTotp();
  const confirmTotp = useConfirmTotp();
  const regenerate = useRegenerateRecoveryCodes();
  // Account-security actions need a recently proven factor; this re-asks
  // and repeats the action rather than failing it (backlog 1.28).
  const stepUp = useStepUp();
  const [passwordOpen, setPasswordOpen] = useState(false);
  const [passkeyOpen, setPasskeyOpen] = useState(false);
  const [enrolling, setEnrolling] = useState<TotpEnrolment | null>(null);
  const [code, setCode] = useState('');
  const [recoveryCodes, setRecoveryCodes] = useState<string[] | null>(null);
  const [askRegenerate, setAskRegenerate] = useState(false);

  const list = passkeys.data?.passkeys ?? [];
  const totpConfirmed = me.data?.totp_confirmed === true;
  const hasSecondFactor = totpConfirmed || list.length > 0;
  // A TOTP confirm on the enrolment page completes the session while the
  // recovery codes are still on screen. If the account must also change
  // its password, the API refuses every enrolment route from then on, so
  // no passkey is offered until the change is done.
  const passwordChangePending = me.data?.mfa_complete === true && me.data?.must_change_password === true;

  function leaveTotp() {
    setEnrolling(null);
    enrolTotp.reset();
    setCode('');
  }

  async function saved() {
    setRecoveryCodes(null);
    setCode('');
    // The recovery codes used to be undismissable, and clearing this
    // component's own state does not clear TanStack Query's mutation
    // cache — reset() does.
    confirmTotp.reset();
    regenerate.reset();
    await onEnrolled?.();
  }

  async function confirm() {
    try {
      const res = await confirmTotp.mutateAsync(code);
      setRecoveryCodes(res.recovery_codes);
      if (res.tokens_revoked) toast.show(t.account.tokensRevokedOnFirstFactor, 'warn');
      setEnrolling(null);
      // The secret is consumed the moment confirmation succeeds.
      enrolTotp.reset();
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.auth.errors.invalidCode, 'destructive');
    }
  }

  // The codes in use stop working the moment new ones exist, so this asks
  // first (R7).
  async function regenerateCodes() {
    setAskRegenerate(false);
    try {
      const res = await regenerate.mutateAsync();
      setRecoveryCodes(res.recovery_codes);
    } catch {
      toast.show(t.account.recovery.regenerateFailed, 'destructive');
    }
  }

  async function register(name: string): Promise<boolean> {
    let ok = false;
    await stepUp.run(
      async () => {
        const { tokensRevoked } = await registerPasskey(name, t);
        toast.show(tokensRevoked ? t.account.tokensRevokedOnFirstFactor : t.account.passkeys.addedToast, tokensRevoked ? 'warn' : 'default');
        ok = true;
        await passkeys.refetch();
        await onEnrolled?.();
      },
      (err) => toast.show(err instanceof Error ? err.message : t.account.passkeys.addFailed, 'destructive'),
    );
    return ok;
  }

  return (
    <SettingsCard
      title={t.account.signIn.title}
      description={t.account.signIn.description}
      badge={
        <Pill tone={hasSecondFactor ? 'ok' : 'warn'}>
          {hasSecondFactor
            ? t.account.secondFactorActive
            : me.data?.password_step_up
              ? t.account.secondFactorOptional
              : t.account.secondFactorRequired}
        </Pill>
      }
    >
      {!onEnrolled && (
        <SettingsRow label={t.account.password.title} help={t.account.password.intro}>
          <Button onClick={() => setPasswordOpen(true)}>{t.account.password.open}</Button>
        </SettingsRow>
      )}

      <SettingsRow label={t.account.totp.title} help={t.account.totp.intro}>
        <div className="flex items-center gap-2">
          <Pill tone={totpConfirmed ? 'ok' : 'unknown'}>{totpConfirmed ? t.account.totp.status : t.account.totp.notSetUp}</Pill>
          <Button
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
            {totpConfirmed ? t.account.totp.enrolAgain : t.account.totp.enrol}
          </Button>
        </div>
      </SettingsRow>

      {totpConfirmed && (
        <SettingsRow
          label={t.account.recovery.title}
          help={
            <>
              <span data-testid="recovery-codes-remaining" className="font-mono text-foreground">
                {me.data?.recovery_codes_remaining ?? 0}
              </span>{' '}
              {t.account.recovery.left}{t.common.dash}{t.account.recovery.leftHint}
            </>
          }
        >
          <Button disabled={regenerate.isPending} onClick={() => setAskRegenerate(true)}>
            {t.account.recovery.regenerate}
          </Button>
        </SettingsRow>
      )}

      <SettingsRow label={t.account.passkeys.title} help={t.account.passkeys.intro}>
        <div className="flex items-center gap-2">
          <Pill tone={list.length > 0 ? 'ok' : 'unknown'}>{t.account.passkeys.count(list.length)}</Pill>
          {!passwordChangePending && <Button onClick={() => setPasskeyOpen(true)}>{t.account.passkeys.add}</Button>}
        </div>
      </SettingsRow>
      {list.map((p) => (
        <div key={p.id} className="flex items-center gap-3 border-t border-border bg-background/40 py-2.5 pl-11 pr-5">
          <KeyRound size={15} className="text-muted-foreground" />
          <div className="flex flex-1 flex-col">
            <span className="text-xs font-semibold">{p.name}</span>
            <span className="text-[11px] text-muted-foreground">{t.account.passkeys.added(fmt.date(p.created_at))}</span>
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

      <PasswordDialog open={passwordOpen} onClose={() => setPasswordOpen(false)} />
      <PasskeyDialog open={passkeyOpen} onClose={() => setPasskeyOpen(false)} onRegister={register} />
      <TotpDialog
        enrolling={enrolling}
        recoveryCodes={recoveryCodes}
        code={code}
        onCode={setCode}
        onConfirm={() => void confirm()}
        onSaved={() => void saved()}
        onClose={() => {
          // Also fires after the dialog was closed by the code itself. The
          // recovery codes are left only through "I have saved these".
          if (!recoveryCodes && enrolling) leaveTotp();
        }}
      />
      {/* Its own question rather than useConfirm: this card is also shown
          where no ConfirmProvider is mounted above it. */}
      <Dialog open={askRegenerate} onClose={() => setAskRegenerate(false)} title={t.ui.confirm.title}>
        <p className="text-[13px] text-foreground">{t.account.recovery.regenerateConfirm}</p>
        <div className="mt-5 flex justify-end gap-2">
          <Button type="button" onClick={() => setAskRegenerate(false)}>
            {t.ui.confirm.cancel}
          </Button>
          <Button type="button" variant="destructive" onClick={() => void regenerateCodes()}>
            {t.account.recovery.regenerateConfirmLabel}
          </Button>
        </div>
      </Dialog>
      {stepUp.dialog}
    </SettingsCard>
  );
}
