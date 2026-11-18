import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { PinForm } from '../lock/PinForm.tsx';
import { useT } from '../../i18n/useT.ts';

/**
 * PinDialog is the lock PIN's set/change flow: the four-box form in a
 * dialog. `onSubmit` throws to refuse (PinForm shows it); on success the
 * caller closes the dialog.
 */
export function PinDialog({
  open,
  replacing,
  onClose,
  onSubmit,
}: {
  open: boolean;
  replacing: boolean;
  onClose: () => void;
  onSubmit: (pin: string) => Promise<void>;
}) {
  const t = useT();
  return (
    <Dialog open={open} onClose={onClose} title={replacing ? t.account.lockPin.dialogChange : t.account.lockPin.dialogSet}>
      {open && (
        <PinForm
          idPrefix="account-lock-pin"
          onSubmit={onSubmit}
          extra={
            <Button type="button" variant="ghost" onClick={onClose}>
              {t.account.lockPin.cancel}
            </Button>
          }
        />
      )}
    </Dialog>
  );
}
