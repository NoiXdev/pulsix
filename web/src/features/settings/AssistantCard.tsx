import { useAssistantSettings, useUpdateAssistantSettings } from '../../api/hooks/useAssistantSettings.ts';
import { SettingsRow } from '../../ui/settings.tsx';
import { Switch } from '../../ui/switch.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useT } from '../../i18n/useT.ts';

/**
 * AssistantRows are the two assistant switch rows of Settings → General
 * (spec 2026-11-16): while assistant access is off, no assistant token of
 * the organization works, whatever its areas; the follow-up switch below it
 * is indented and disabled until access is on. Every member sees the state;
 * only a role with org.settings.write (admins and owners) may change it, the
 * same gate the server applies. A change is saved at once, like any switch.
 */
export function AssistantRows({ org, canWrite }: { org: string; canWrite: boolean }) {
  const t = useT();
  const a = t.settings.assistant;
  const toast = useToast();
  const settings = useAssistantSettings(org);
  const update = useUpdateAssistantSettings(org);
  const enabled = settings.data?.settings.assistant_enabled ?? false;
  const startsFollowUps = settings.data?.settings.approved_runs_start_follow_ups ?? false;
  const busy = !canWrite || settings.isPending || update.isPending;

  async function onToggle(next: boolean) {
    try {
      await update.mutateAsync({ assistant_enabled: next });
      toast.show(next ? a.turnedOn : a.turnedOff);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : a.saveFailed, 'destructive');
    }
  }

  async function onToggleFollowUps(next: boolean) {
    try {
      await update.mutateAsync({ approved_runs_start_follow_ups: next });
      toast.show(next ? a.followUpsOn : a.followUpsOff);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : a.followUpsSaveFailed, 'destructive');
    }
  }

  if (settings.isError) {
    return (
      <p role="alert" className="border-t border-border px-5 py-3.5 text-xs text-destructive">
        {a.loadFailed}
      </p>
    );
  }
  return (
    <>
      <SettingsRow label={a.title} help={a.hint}>
        {(helpId) => (
          <Switch
            aria-label={a.title}
            aria-describedby={helpId}
            checked={enabled}
            disabled={busy}
            onCheckedChange={(next) => void onToggle(next)}
          />
        )}
      </SettingsRow>
      <SettingsRow label={a.followUpsTitle} help={a.followUpsHint} indent>
        {(helpId) => (
          <Switch
            aria-label={a.followUpsTitle}
            aria-describedby={helpId}
            checked={startsFollowUps}
            disabled={busy || !enabled}
            onCheckedChange={(next) => void onToggleFollowUps(next)}
          />
        )}
      </SettingsRow>
    </>
  );
}
