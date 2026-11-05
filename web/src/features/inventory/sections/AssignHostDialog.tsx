import { useState, type FormEvent } from 'react';
import { Dialog } from '../../../ui/dialog.tsx';
import { Button } from '../../../ui/button.tsx';
import { useToast } from '../../../ui/toastContext.ts';
import { errorStatus } from '../../../lib/apiError.ts';
import { toVarSchema, useAssignHost } from '../../../api/hooks/useInventory.ts';
import type { InventoryGroup, InventoryMember } from '../../../api/types.ts';
import { SchemaForm } from '../../variables/SchemaForm.tsx';
import { groupFields, membershipFields, problemsFromError } from '../../variables/paths.ts';
import { declaredOnly, toKeepMarkers } from '../../variables/secrets.ts';
import type { Problem, Vars } from '../../variables/types.ts';
import { ManagedHostPicker } from './ManagedHostPicker.tsx';
import { useT } from '../../../i18n/useT.ts';

/** AssignHostDialog is the generated assignment form (SchemaBuilder mockup,
 * right column): a managed host, the group's host-scoped fields and
 * overrides, and the group's own values read-only. With a member it edits
 * that member's values instead. */
export function AssignHostDialog({
  org, group, member, existing, onClose,
}: {
  org: string;
  group: InventoryGroup;
  member?: InventoryMember;
  existing: readonly string[];
  onClose: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const schema = toVarSchema(group.schema);
  const fields = membershipFields(schema);
  const ownFields = groupFields(schema);
  const assign = useAssignHost(org, group.id);
  const [hostId, setHostId] = useState(member?.host_id ?? '');
  const [vars, setVars] = useState<Vars>(() => (member ? toKeepMarkers(fields, declaredOnly(fields, (member.vars ?? {}) as Vars)) : {}));
  const [problems, setProblems] = useState<Problem[]>(() => (member?.problems ?? []).map((p) => ({ path: p.path, message: p.message })));
  const [error, setError] = useState<string | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setProblems([]);
    setError(null);
    try {
      await assign.mutateAsync({ hostId, vars: declaredOnly(fields, vars), revision: member?.revision });
      assign.reset();
      toast.show(member ? t.inventory.assign.valuesSaved(member.host_name) : t.inventory.assign.assigned(group.name));
      onClose();
    } catch (err) {
      if (errorStatus(err) === 412) return setError(t.inventory.assign.stale);
      const ps = problemsFromError(err, 'body.vars');
      if (ps.length > 0) setProblems(ps);
      else setError(err instanceof Error ? err.message : t.inventory.assign.saveFailed);
    }
  }

  return (
    <Dialog open onClose={onClose} size="wide" title={member ? t.inventory.assign.titleValues(member.host_name) : t.inventory.assign.titleAssign(group.name)}>
      <form onSubmit={submit} className="flex max-h-[70vh] flex-col gap-4 overflow-y-auto">
        {!member && <ManagedHostPicker org={org} existing={existing} value={hostId} onChange={setHostId} />}
        <SchemaForm fields={fields} value={vars} onChange={setVars} problems={problems} idPrefix="assign" />
        {ownFields.length > 0 && (
          <details className="rounded-md border border-border p-3">
            <summary className="cursor-pointer text-xs font-semibold">{t.inventory.assign.groupValues}</summary>
            <div className="pt-3">
              <SchemaForm fields={ownFields} value={toKeepMarkers(ownFields, (group.vars ?? {}) as Vars)} onChange={() => undefined} disabled idPrefix="group-ro" />
            </div>
          </details>
        )}
        {error && (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>
            {t.common.cancel}
          </Button>
          <Button type="submit" variant="primary" disabled={!hostId || assign.isPending}>
            {member ? t.inventory.assign.saveValues : t.inventory.assign.submit(group.name)}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
