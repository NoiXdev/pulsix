import { Button } from '../../ui/button.tsx';
import { useConfirm } from '../../ui/confirmContext.ts';
import { useToast } from '../../ui/toastContext.ts';
import { useDiscardPendingTrigger, useRunPendingTrigger, useTriggerPending } from '../../api/hooks/useTriggers.ts';
import type { Trigger } from '../../api/types.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';

/**
 * PendingRun is a settling trigger's collected run while it waits (backlog
 * 2.137): how many deliveries it holds, when it starts unless more arrive
 * and the latest it starts at. "Run now" starts it at once, as the person
 * (canRun: the test call's permission); "Discard" drops it after asking
 * (canDiscard). Nothing shows while none waits; a trigger without a
 * settle delay is not asked.
 */
export function PendingRun({ org, trigger, canRun, canDiscard }: { org: string; trigger: Trigger; canRun: boolean; canDiscard: boolean }) {
  const t = useT();
  const m = t.triggers.settle;
  const fmt = useFormat();
  const toast = useToast();
  const ask = useConfirm();
  const pending = useTriggerPending(org, trigger.id, (trigger.settle_seconds ?? 0) > 0).data?.pending;
  const run = useRunPendingTrigger(org);
  const discard = useDiscardPendingTrigger(org);
  if (!pending) return null;

  async function onRun() {
    try {
      const res = await run.mutateAsync(trigger.id);
      const outcome = res.outcome === 'failed' || res.outcome === 'disabled' ? res.outcome : 'triggered';
      toast.show(m.started[outcome], outcome === 'triggered' ? undefined : 'warn');
    } catch (err) {
      toast.show(err instanceof Error ? err.message : m.runNowFailed, 'destructive');
    }
  }

  async function onDiscard() {
    if (!(await ask({ message: m.discardConfirm(trigger.name), confirmLabel: m.discard, destructive: true }))) return;
    try {
      await discard.mutateAsync(trigger.id);
      toast.show(m.discarded);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : m.discardFailed, 'destructive');
    }
  }

  return (
    <section aria-label={m.pendingTitle} className="flex flex-col gap-1 rounded-md border border-[var(--px-warn)] px-3 py-2 text-xs">
      <p className="font-semibold">{m.pendingTitle}</p>
      <p>{m.deliveries(pending.deliveries)}</p>
      <p>{m.due(fmt.dateTime(pending.due_at))}</p>
      <p className="text-muted-foreground">{m.deadline(fmt.dateTime(pending.deadline_at))}</p>
      {(canRun || canDiscard) && (
        <div className="mt-1 flex flex-wrap gap-2">
          {canRun && (
            <Button type="button" disabled={run.isPending} onClick={() => void onRun()}>
              {m.runNow}
            </Button>
          )}
          {canDiscard && (
            <Button type="button" variant="destructive" disabled={discard.isPending} onClick={() => void onDiscard()}>
              {m.discard}
            </Button>
          )}
        </div>
      )}
    </section>
  );
}
