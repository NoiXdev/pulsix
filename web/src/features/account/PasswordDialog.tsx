import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useT } from '../../i18n/useT.ts';
import { PasswordChangeForm } from './PasswordChangeForm.tsx';

/**
 * PasswordDialog is the Password row's flow on Account security: changing
 * one's own password with the current one. It needs a fresh second factor,
 * and every other session of the account ends; this one stays, and the
 * toast says so.
 */
export function PasswordDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  return (
    <Dialog open={open} onClose={onClose} title={t.account.password.submit}>
      {open && (
        <PasswordChangeForm
          variant="own"
          onChanged={() => {
            toast.show(t.account.password.changed);
            onClose();
          }}
          extra={
            <Button type="button" variant="ghost" onClick={onClose}>
              {t.common.cancel}
            </Button>
          }
        />
      )}
    </Dialog>
  );
}
