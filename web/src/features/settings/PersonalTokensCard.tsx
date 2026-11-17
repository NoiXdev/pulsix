import { usePersonalTokenSetting, useSetPersonalTokenSetting } from '../../api/hooks/usePersonalTokens.ts';
import { Switch } from '../../ui/switch.tsx';
import { Card } from '../../ui/table.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useT } from '../../i18n/useT.ts';

/**
 * PersonalTokensCard is the organization's switch "Allow personal tokens" on
 * Settings → General (backlog 3.100): off, no member's personal token works
 * here and no new grant is accepted; on again, they work again, except the
 * ones an administrator revoked. Only a role with org.settings.write (admins
 * and owners) sees the switch, the gate the server applies; everyone else
 * sees the state. A change is saved at once.
 */
export function PersonalTokensCard({ org, canWrite }: { org: string; canWrite: boolean }) {
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

  return (
    <Card>
      <div className="flex flex-col gap-3 p-4">
        <div className="flex items-center justify-between gap-3">
          <h2 className="text-sm font-semibold">{s.title}</h2>
          {setting.isSuccess &&
            (canWrite ? (
              <Switch
                aria-label={s.title}
                aria-describedby="personal_tokens_hint"
                checked={allowed}
                disabled={update.isPending}
                onCheckedChange={(next) => void onToggle(next)}
              />
            ) : (
              <span className="text-xs font-semibold">{allowed ? s.on : s.off}</span>
            ))}
        </div>
        {setting.isError ? (
          <p className="text-xs text-destructive">{s.loadFailed}</p>
        ) : (
          <span id="personal_tokens_hint" className="text-xs text-muted-foreground">
            {s.hint}
          </span>
        )}
        {!canWrite && <span className="text-xs text-muted-foreground">{s.readOnlyHint}</span>}
      </div>
    </Card>
  );
}
