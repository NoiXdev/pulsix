import { Card } from '../../ui/table.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useT } from '../../i18n/useT.ts';
import { PasswordChangeForm } from './PasswordChangeForm.tsx';

/**
 * PasswordCard is Account security's Password card: changing one's own
 * password with the current one. It needs a fresh second factor, and every
 * other session of the account ends; this one stays, and the toast says so.
 */
export function PasswordCard() {
  const t = useT();
  const toast = useToast();
  return (
    <Card>
      <div className="flex flex-col gap-3 p-4">
        <h2 className="text-sm font-semibold">{t.account.password.title}</h2>
        <p className="text-[11px] text-muted-foreground">{t.account.password.intro}</p>
        <PasswordChangeForm variant="own" onChanged={() => toast.show(t.account.password.changed)} />
      </div>
    </Card>
  );
}
