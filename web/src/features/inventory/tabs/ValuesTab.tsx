import { useState } from 'react';
import { Button } from '../../../ui/button.tsx';
import { useToast } from '../../../ui/toastContext.ts';
import { errorStatus } from '../../../lib/apiError.ts';
import { toVarSchema, useSetGroupVars } from '../../../api/hooks/useInventory.ts';
import type { InventoryGroup } from '../../../api/types.ts';
import { SchemaForm } from '../schema/SchemaForm.tsx';
import { groupFields, problemsFromError } from '../schema/paths.ts';
import { declaredOnly, toKeepMarkers } from '../schema/secrets.ts';
import type { Problem, Vars } from '../schema/types.ts';
import { StaleNotice } from './StaleNotice.tsx';

export function ValuesTab({ org, group }: { org: string; group: InventoryGroup }) {
  const toast = useToast();
  const fields = groupFields(toVarSchema(group.schema));
  const save = useSetGroupVars(org, group.id);
  const [draft, setDraft] = useState<Vars>(() => toKeepMarkers(fields, declaredOnly(fields, (group.vars ?? {}) as Vars)));
  const [problems, setProblems] = useState<Problem[]>(() => (group.problems ?? []).map((p) => ({ path: p.path, message: p.message })));
  const [stale, setStale] = useState(false);

  async function onSave() {
    setProblems([]);
    try {
      await save.mutateAsync({ vars: declaredOnly(fields, draft), revision: group.revision });
      save.reset();
      toast.show('Group values saved');
    } catch (err) {
      if (errorStatus(err) === 412) return setStale(true);
      const ps = problemsFromError(err, 'body.vars');
      if (ps.length > 0) setProblems(ps);
      else toast.show(err instanceof Error ? err.message : 'Saving the values failed', 'destructive');
    }
  }

  if (fields.length === 0) {
    return <p className="text-xs text-muted-foreground">This group's schema has no group-scoped fields yet. Add them on the Variable schema tab.</p>;
  }
  return (
    <div className="flex max-w-[640px] flex-col gap-4">
      {stale && <StaleNotice org={org} />}
      {(group.orphaned ?? []).length > 0 && (
        <p className="rounded-md border border-border bg-card px-3 py-2 text-xs text-muted-foreground">
          Values of removed fields are still stored: <span className="font-mono">{(group.orphaned ?? []).join(', ')}</span>. Saving this form drops them.
        </p>
      )}
      <SchemaForm fields={fields} value={draft} onChange={setDraft} problems={problems} idPrefix="values" />
      <div className="flex justify-end">
        <Button variant="primary" disabled={save.isPending} onClick={() => void onSave()}>
          Save values
        </Button>
      </div>
    </div>
  );
}
