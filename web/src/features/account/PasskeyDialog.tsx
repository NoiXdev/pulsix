import { useState } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Field } from '../../ui/field.tsx';
import { useT } from '../../i18n/useT.ts';

/**
 * PasskeyDialog asks for the name of a passkey about to be registered.
 * `onRegister` runs the registration (step-up and the browser's prompt
 * included) and resolves once it succeeded, which closes the dialog; on a
 * failure it reports itself and the dialog stays open.
 */
export function PasskeyDialog({
  open,
  onClose,
  onRegister,
}: {
  open: boolean;
  onClose: () => void;
  onRegister: (name: string) => Promise<boolean>;
}) {
  const t = useT();
  return (
    <Dialog open={open} onClose={onClose} title={t.account.passkeys.dialogTitle}>
      {open && <PasskeyForm onClose={onClose} onRegister={onRegister} />}
    </Dialog>
  );
}

function PasskeyForm({ onClose, onRegister }: { onClose: () => void; onRegister: (name: string) => Promise<boolean> }) {
  const t = useT();
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  return (
    <form
      className="flex flex-col gap-4"
      onSubmit={(e) => {
        e.preventDefault();
        if (!name.trim() || busy) return;
        setBusy(true);
        void onRegister(name.trim())
          .then((ok) => {
            if (ok) onClose();
          })
          .finally(() => setBusy(false));
      }}
    >
      <p className="text-xs text-muted-foreground">{t.account.passkeys.intro}</p>
      <Field id="passkey-name" label={t.account.passkeys.nameLabel}>
        <Input
          id="passkey-name"
          autoFocus
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={t.account.passkeys.namePlaceholder}
        />
      </Field>
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" onClick={onClose}>
          {t.account.passkeys.cancel}
        </Button>
        <Button type="submit" variant="primary" disabled={busy || !name.trim()}>
          {t.account.passkeys.register}
        </Button>
      </div>
    </form>
  );
}
