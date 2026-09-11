import { useState, type FormEvent } from 'react';
import { Dialog } from '../../../ui/dialog.tsx';
import { Button } from '../../../ui/button.tsx';
import { Label } from '../../../ui/label.tsx';
import { useToast } from '../../../ui/toastContext.ts';
import { errorStatus } from '../../../lib/apiError.ts';
import { useHosts } from '../../../api/hooks/useHosts.ts';
import { toVarSchema, useAssignHost } from '../../../api/hooks/useInventory.ts';
import type { InventoryGroup, InventoryMember } from '../../../api/types.ts';
import { SchemaForm } from '../schema/SchemaForm.tsx';
import { groupFields, membershipFields, problemsFromError } from '../schema/paths.ts';
import { declaredOnly, toKeepMarkers } from '../schema/secrets.ts';
import type { Problem, Vars } from '../schema/types.ts';

const SELECT = 'h-8 w-full rounded-md border border-border bg-background px-2 text-xs text-foreground';

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
  const toast = useToast();
  const schema = toVarSchema(group.schema);
  const fields = membershipFields(schema);
  const ownFields = groupFields(schema);
  const hosts = useHosts(org, { managed: true });
  const assignable = (hosts.data?.pages.flatMap((p) => p.hosts ?? []) ?? []).filter((h) => !existing.includes(h.id));
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
      toast.show(member ? `Values of ${member.host_name} saved` : `Host assigned to ${group.name}`);
      onClose();
    } catch (err) {
      if (errorStatus(err) === 412) return setError('These values changed since you opened them. Close the dialog, reload and try again.');
      const ps = problemsFromError(err, 'body.vars');
      if (ps.length > 0) setProblems(ps);
      else setError(err instanceof Error ? err.message : 'Saving failed');
    }
  }

  return (
    <Dialog open onClose={onClose} size="wide" title={member ? `Values of ${member.host_name}` : `Assign host to ${group.name}`}>
      <form onSubmit={submit} className="flex max-h-[70vh] flex-col gap-4 overflow-y-auto">
        {!member && (
          <div className="flex flex-col gap-1">
            <Label htmlFor="assign-host">Host</Label>
            <select id="assign-host" className={SELECT} required value={hostId} onChange={(e) => setHostId(e.target.value)}>
              <option value="">Choose a managed host…</option>
              {assignable.map((h) => (
                <option key={h.id} value={h.id}>
                  {h.name}
                </option>
              ))}
            </select>
            {hosts.data && assignable.length === 0 && <span className="text-[11px] text-muted-foreground">Every managed host is already a member.</span>}
          </div>
        )}
        <SchemaForm fields={fields} value={vars} onChange={setVars} problems={problems} idPrefix="assign" />
        {ownFields.length > 0 && (
          <details className="rounded-md border border-border p-3">
            <summary className="cursor-pointer text-xs font-semibold">Group values (read-only)</summary>
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
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={!hostId || assign.isPending}>
            {member ? 'Save values' : `Assign to ${group.name}`}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
