import type { Trigger, TriggerInput } from '../../api/types.ts';
import { useDeleteTrigger, useRotateTriggerSecret, useUpdateTrigger } from '../../api/hooks/useTriggers.ts';
import { useT } from '../../i18n/useT.ts';
import { useConfirm } from '../../ui/confirmContext.ts';
import { useToast } from '../../ui/toastContext.ts';

/** A secret a trigger was just given, with the URL its sender calls; shown
 * once. */
export interface FreshSecret {
  secret: string;
  url: string;
}

/**
 * useTriggerCommands is what a person does to one webhook trigger, as the
 * triggers dialog and the template page's Triggers tab offer it: rotate its
 * secret (after a confirmation; the new secret is returned to be shown
 * once), delete it (after a confirmation) and save an edit under If-Match.
 * A failed rotation or deletion is a toast; a failed save returns its
 * message for the form to show.
 */
export function useTriggerCommands(org: string, trigger: Trigger) {
  const t = useT();
  const m = t.triggers;
  const toast = useToast();
  const ask = useConfirm();
  const update = useUpdateTrigger(org, trigger.id);
  const del = useDeleteTrigger(org);
  const rotateSecret = useRotateTriggerSecret(org);

  async function rotate(): Promise<FreshSecret | null> {
    if (!(await ask({ message: m.rotateConfirm(trigger.name), confirmLabel: m.rotate }))) return null;
    try {
      const res = await rotateSecret.mutateAsync(trigger.id);
      return { secret: res.secret, url: res.url || trigger.url };
    } catch (err) {
      toast.show(err instanceof Error ? err.message : m.rotateFailed, 'destructive');
      return null;
    }
  }

  async function remove(): Promise<boolean> {
    if (!(await ask({ message: m.deleteConfirm(trigger.name), confirmLabel: t.common.delete, destructive: true }))) return false;
    try {
      await del.mutateAsync(trigger.id);
      return true;
    } catch (err) {
      toast.show(err instanceof Error ? err.message : m.deleteFailed, 'destructive');
      return false;
    }
  }

  /** save returns null when saved, else what went wrong. */
  async function save(body: TriggerInput): Promise<string | null> {
    try {
      await update.mutateAsync({ ...body, revision: trigger.revision });
      return null;
    } catch (err) {
      return err instanceof Error ? err.message : m.savingFailed;
    }
  }

  return { rotate, remove, save, saving: update.isPending, removing: del.isPending, rotating: rotateSecret.isPending };
}
