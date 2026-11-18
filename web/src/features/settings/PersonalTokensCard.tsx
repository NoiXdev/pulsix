import { usePersonalTokenSetting, useSetPersonalTokenSetting } from '../../api/hooks/usePersonalTokens.ts';
import { SettingsRow } from '../../ui/settings.tsx';
import { Switch } from '../../ui/switch.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useT } from '../../i18n/useT.ts';

/**
 * PersonalTokensRow is the switch row "Allow personal tokens" on Settings →
 * General (backlog 3.100): off, no member's personal token works here and no
 * new grant is accepted; on again, they work again, except the ones an
 * administrator revoked. Only a role with org.settings.write (admins and
 * owners) may change it, the gate the server applies; everyone else sees the
 * state in a disabled switch. A change is saved at once.
 */
export function PersonalTokensRow({ org, canWrite }: { org: string; canWrite: boolean }) {
  const t = useT();
  const s = t.tokens.personal.setting;
  const toast = useToast();
  const setting = usePersonalTokenSetting(org);
  const update = useSetPersonalTokenSetting(org);
  const allowed = setting.data?.allowed ?? true;

  async function onToggle(next: boolean) {
    try {
      await update.mutateAsync(next);
      toast.show(next ? s.turnedOn : s.turnedOff);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : s.saveFailed, 'destructive');
    }
  }

  if (setting.isError) {
    return (
      <p role="alert" className="border-t border-border px-5 py-3.5 text-xs text-destructive">
        {s.loadFailed}
      </p>
    );
  }
  return (
    <SettingsRow label={s.title} help={s.hint}>
      {(helpId) =>
        setting.isSuccess ? (
          <Switch
            aria-label={s.title}
            aria-describedby={helpId}
            checked={allowed}
            disabled={!canWrite || update.isPending}
            onCheckedChange={(next) => void onToggle(next)}
          />
        ) : null
      }
    </SettingsRow>
  );
}
