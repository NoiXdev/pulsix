import { useAssistantSettings, useUpdateAssistantSettings } from '../../api/hooks/useAssistantSettings.ts';
import { Switch } from '../../ui/switch.tsx';
import { Card } from '../../ui/table.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useT } from '../../i18n/useT.ts';

/**
 * AssistantCard is the organization's assistant access switch on Settings →
 * General (spec 2026-11-16): while it is off, no assistant token of the
 * organization works, whatever its areas. Every member sees the state; only
 * a role with org.settings.write (admins and owners) may change it, the same
 * gate the server applies. A change is saved at once, like any switch.
 */
export function AssistantCard({ org, canWrite }: { org: string; canWrite: boolean }) {
  const t = useT();
  const a = t.settings.assistant;
  const toast = useToast();
  const settings = useAssistantSettings(org);
  const update = useUpdateAssistantSettings(org);
  const enabled = settings.data?.settings.assistant_enabled ?? false;

  async function onToggle(next: boolean) {
    try {
      await update.mutateAsync(next);
      toast.show(next ? a.turnedOn : a.turnedOff);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : a.saveFailed, 'destructive');
    }
  }

  return (
    <Card>
      <div className="flex flex-col gap-3 p-4">
        <div className="flex items-center justify-between gap-3">
          <h2 id="assistant_access_heading" className="text-sm font-semibold">
            {a.title}
          </h2>
          {!settings.isError && (
            <Switch
              aria-label={a.title}
              aria-describedby="assistant_access_hint"
              checked={enabled}
              disabled={!canWrite || settings.isPending || update.isPending}
              onCheckedChange={(next) => void onToggle(next)}
            />
          )}
        </div>
        {settings.isError ? (
          <p role="alert" className="text-xs text-destructive">
            {a.loadFailed}
          </p>
        ) : (
          <span id="assistant_access_hint" className="text-xs text-muted-foreground">
            {a.hint}
          </span>
        )}
        {!canWrite && <span className="text-xs text-muted-foreground">{a.readOnlyHint}</span>}
      </div>
    </Card>
  );
}
