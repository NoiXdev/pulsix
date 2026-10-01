import { Shield, ShieldOff } from 'lucide-react';
import { useQueryClient } from '@tanstack/react-query';
import { Button } from '../../ui/button.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useCreateItemDecision, useUpdateItemDecision } from '../../api/hooks/useItems.ts';
import type { ItemDecision, ItemDecisionInput } from '../../api/types.ts';

export interface ItemDecisionButtonProps {
  org: string;
  hostId: string;
  /** The check this item belongs to — a decision names `(check_key, item)`
   * together, never an item alone (two checks can share a mount name, a
   * unit name does not collide with anything else). */
  checkKey: string;
  /** The item this row is for. Never called for a whole-host metric — see
   * HostChecksCard's own ValueRow, which renders this only when
   * `value.item !== ''`: items with an empty name are not rows at all
   * (v0.3b-3 design, "Items with an empty name"). */
  item: string;
  /** This tuple's current effective verdict — HostCheckValueDTO's own
   * `ignored`, already resolved server-side. Decides both the label
   * ("Ignore" vs "Watch") and which verdict the click writes. */
  ignored: boolean;
  /** The decision currently governing this item, if any — ItemDTO's own
   * `decided_by`, looked up by HostChecksCard from GET /hosts/{id}/items.
   * Absent means nothing decides this item yet (the default, watch). */
  decidedBy?: ItemDecision;
}

/** isOwnHostExactDecision reports whether `decidedBy` is a decision this
 * very button could have written before: host-scoped, exact, naming
 * exactly this check and item. Only then is flipping it a PUT to the same
 * row — anything broader or narrower (an organization pattern, a host
 * glob) is left untouched and overruled by a brand new decision instead,
 * never edited by a control this specific. */
function isOwnHostExactDecision(decidedBy: ItemDecision | undefined, checkKey: string, item: string): decidedBy is ItemDecision {
  return (
    decidedBy !== undefined &&
    decidedBy.scope === 'host' &&
    decidedBy.match_kind === 'exact' &&
    decidedBy.check_key === checkKey &&
    decidedBy.pattern === item
  );
}

/**
 * ItemDecisionButton is the row's own fast path (v0.3b-3 spec, "Host page,
 * monitoring card"): an operator looking at this value does not compose a
 * pattern on the Items tab, they silence or restore the one item in front
 * of them. It always writes a host-scoped, EXACT decision — the most
 * specific kind there is, so it reliably outranks whatever organization
 * pattern (or nothing at all) decided the row before (review focus 3: "an
 * organization pattern and a host watch on the same item").
 *
 * A second click on a row this exact button already governs updates that
 * same decision rather than stacking a second one beside it:
 * internal/items' own tie-break for two decisions at equal specificity is
 * the OLDER created_at (the same total order rules.Resolve uses), so a
 * plain second POST would silently lose to the first and leave the item
 * exactly where the operator just tried to move it away from. Anything
 * else governing the item — an organization pattern, a host glob — is
 * left alone and simply outranked by the new exact decision; this button
 * only ever edits a decision it could have created itself.
 */
export function ItemDecisionButton({ org, hostId, checkKey, item, ignored, decidedBy }: ItemDecisionButtonProps) {
  const qc = useQueryClient();
  const toast = useToast();
  const own = isOwnHostExactDecision(decidedBy, checkKey, item);
  const create = useCreateItemDecision(org);
  const update = useUpdateItemDecision(org, own ? decidedBy.id : '');
  const pending = create.isPending || update.isPending;

  async function onClick() {
    const body: ItemDecisionInput = {
      scope: 'host',
      host_id: hostId,
      check_key: checkKey,
      match_kind: 'exact',
      pattern: item,
      decision: ignored ? 'watch' : 'ignore',
      // Carried over, not reset: flipping the verdict is not the operator
      // disowning why it was set this way in the first place.
      ...(own && decidedBy.note ? { note: decidedBy.note } : {}),
    };
    try {
      if (own) await update.mutateAsync(body);
      else await create.mutateAsync(body);
      // Neither useHostChecks, useHostItems nor useHostEffectiveRules is
      // invalidated by useCreateItemDecision/useUpdateItemDecision — those
      // hooks only know about the org-wide Items tab's own list. Without
      // this the row would sit wrong (or merely "not yet evaluated") for
      // up to useHostChecks' own 30 s poll, which reads as "I ignored it
      // and nothing happened" for an action this fast is supposed to make
      // feel immediate.
      await Promise.all([
        qc.invalidateQueries({ queryKey: ['host-checks', org, hostId] }),
        qc.invalidateQueries({ queryKey: ['host-items', org, hostId] }),
        qc.invalidateQueries({ queryKey: ['host-effective-rules', org, hostId] }),
      ]);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : 'Saving this decision failed', 'destructive');
    }
  }

  return (
    <Button
      type="button"
      variant="ghost"
      aria-label={`${ignored ? 'Watch' : 'Ignore'} ${checkKey} ${item}`}
      disabled={pending}
      onClick={() => void onClick()}
    >
      {ignored ? (
        <>
          <Shield size={14} /> Watch
        </>
      ) : (
        <>
          <ShieldOff size={14} /> Ignore
        </>
      )}
    </Button>
  );
}
