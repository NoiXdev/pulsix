import { QRCodeSVG } from 'qrcode.react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { useT } from '../../i18n/useT.ts';

export interface TotpEnrolment {
  secret: string;
  otpauth_url: string;
}

/**
 * TotpDialog walks the authenticator app flow: scan the secret and confirm
 * it with a code, then the recovery codes, which are shown once. It is
 * open while either is on screen (`enrolling` or `recoveryCodes` set).
 * The recovery codes cannot be dismissed (no Escape): they are shown
 * once, so only the explicit "I have saved these" button (`onSaved`)
 * leaves that step. `onClose` is for leaving the enrolment step (Cancel,
 * Escape), which forgets the secret.
 */
export function TotpDialog({
  enrolling,
  recoveryCodes,
  code,
  onCode,
  onConfirm,
  onSaved,
  onClose,
}: {
  enrolling: TotpEnrolment | null;
  recoveryCodes: string[] | null;
  code: string;
  onCode: (code: string) => void;
  onConfirm: () => void;
  onSaved: () => void;
  onClose: () => void;
}) {
  const t = useT();
  const title = recoveryCodes ? t.account.recovery.dialogTitle : t.account.totp.dialogTitle;
  return (
    <Dialog open={enrolling !== null || recoveryCodes !== null} onClose={onClose} title={title} persistent={recoveryCodes !== null}>
      {recoveryCodes ? (
        <div className="flex flex-col gap-3">
          <p className="text-xs text-muted-foreground">{t.account.recovery.shownOnce}</p>
          <div className="grid grid-cols-2 gap-1.5 rounded-md border border-border bg-background p-3 font-mono text-xs">
            {recoveryCodes.map((c) => (
              <span key={c}>{c}</span>
            ))}
          </div>
          <div className="flex justify-end">
            <Button variant="primary" onClick={onSaved}>
              {t.account.recovery.saved}
            </Button>
          </div>
        </div>
      ) : enrolling ? (
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            onConfirm();
          }}
        >
          <p className="text-xs text-muted-foreground">{t.account.totp.scanHint}</p>
          <QRCodeSVG
            value={enrolling.otpauth_url}
            size={132}
            fgColor="#0E1117"
            bgColor="#F6F4EF"
            className="self-start rounded-md p-2"
            style={{ background: 'var(--px-paper)' }}
          />
          <code className="break-all rounded-md border border-border bg-background p-2 font-mono text-[11px]">{enrolling.secret}</code>
          <Input
            inputMode="numeric"
            pattern="[0-9]{6}"
            placeholder="482913"
            aria-label={t.account.totp.title}
            value={code}
            onChange={(e) => onCode(e.target.value)}
          />
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              {t.common.cancel}
            </Button>
            <Button type="submit" variant="primary">
              {t.account.confirm}
            </Button>
          </div>
        </form>
      ) : null}
    </Dialog>
  );
}
