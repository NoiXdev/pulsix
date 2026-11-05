import { useState } from 'react';
import { Button } from '../../../ui/button.tsx';
import { useToast } from '../../../ui/toastContext.ts';
import { errorStatus } from '../../../lib/apiError.ts';
import { toVarSchema, useSetGroupSchema } from '../../../api/hooks/useInventory.ts';
import type { InventoryGroup } from '../../../api/types.ts';
import { SchemaBuilder } from '../schema/SchemaBuilder.tsx';
import { problemsFromError } from '../schema/paths.ts';
import type { Problem, VarSchema } from '../schema/types.ts';
import { StaleNotice } from './StaleNotice.tsx';
import { useT } from '../../../i18n/useT.ts';

export function SchemaSection({ org, group }: { org: string; group: InventoryGroup }) {
  const t = useT();
  const toast = useToast();
  const save = useSetGroupSchema(org, group.id);
  const [draft, setDraft] = useState<VarSchema>(() => toVarSchema(group.schema));
  const [problems, setProblems] = useState<Problem[]>([]);
  const [stale, setStale] = useState(false);
  // The builder keeps per-field editor state keyed by field identity; a
  // wholesale reset of the draft (Discard) must remount it, or an editor
  // could keep showing text from before the reset.
  const [builderKey, setBuilderKey] = useState(0);

  async function onSave() {
    setProblems([]);
    try {
      const res = await save.mutateAsync({ schema: draft, revision: group.revision });
      toast.show(res.incomplete_members > 0 ? t.inventory.schemaTab.savedIncomplete(res.incomplete_members) : t.inventory.schemaTab.saved);
    } catch (err) {
      if (errorStatus(err) === 412) return setStale(true);
      const ps = problemsFromError(err, 'body.schema');
      if (ps.length > 0) setProblems(ps);
      else toast.show(err instanceof Error ? err.message : t.inventory.schemaTab.saveFailed, 'destructive');
    }
  }

  return (
    <div className="flex flex-col gap-4">
      {stale && <StaleNotice org={org} />}
      <div className="flex items-center justify-between">
        <span className="text-xs text-muted-foreground">
          {t.inventory.schemaTab.summary(draft.fields.length, draft.fields.filter((f) => f.scope === 'host').length)}
        </span>
        <div className="flex gap-2">
          <Button
            onClick={() => {
              setDraft(toVarSchema(group.schema));
              setProblems([]);
              setBuilderKey((k) => k + 1);
            }}
          >
            {t.inventory.schemaTab.discard}
          </Button>
          <Button variant="primary" disabled={save.isPending} onClick={() => void onSave()}>
            {t.inventory.schemaTab.save}
          </Button>
        </div>
      </div>
      {problems.length > 0 && (
        <p role="alert" className="text-xs text-destructive">
          {t.inventory.schemaTab.problems(problems.length)}
        </p>
      )}
      <SchemaBuilder key={builderKey} value={draft} onChange={setDraft} problems={problems} />
    </div>
  );
}
