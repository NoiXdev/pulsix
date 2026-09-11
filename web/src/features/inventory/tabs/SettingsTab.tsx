import { useState, type FormEvent } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { Button } from '../../../ui/button.tsx';
import { Input } from '../../../ui/input.tsx';
import { Label } from '../../../ui/label.tsx';
import { useToast } from '../../../ui/toastContext.ts';
import { errorStatus } from '../../../lib/apiError.ts';
import { useDeleteGroup, useUpdateGroup } from '../../../api/hooks/useInventory.ts';
import { useBastions } from '../../../api/hooks/useSsh.ts';
import type { InventoryGroup, InventoryGroupSummary } from '../../../api/types.ts';
import { descendantIds, orderTree } from '../tree.ts';
import { StaleNotice } from './StaleNotice.tsx';

const SELECT = 'h-8 w-full rounded-md border border-border bg-background px-2 text-xs text-foreground';

export function SettingsTab({ org, group, groups }: { org: string; group: InventoryGroup; groups: readonly InventoryGroupSummary[] }) {
  const toast = useToast();
  const navigate = useNavigate();
  const bastions = useBastions(org);
  const update = useUpdateGroup(org, group.id);
  const remove = useDeleteGroup(org);
  const [name, setName] = useState(group.name);
  const [description, setDescription] = useState(group.description);
  const [parent, setParent] = useState(group.parent_id ?? '');
  const [bastion, setBastion] = useState(group.bastion_id ?? '');
  const [stale, setStale] = useState(false);
  const excluded = descendantIds(groups, group.id).add(group.id);

  async function save(e: FormEvent) {
    e.preventDefault();
    try {
      await update.mutateAsync({ name, description: description || undefined, parent_id: parent || undefined, bastion_id: bastion || undefined, revision: group.revision });
      toast.show('Group saved');
    } catch (err) {
      if (errorStatus(err) === 412) return setStale(true);
      toast.show(err instanceof Error ? err.message : 'Saving failed', 'destructive');
    }
  }

  async function onDelete() {
    if (!confirm(`Delete group ${group.name}? Its members lose the values they have for this group.`)) return;
    try {
      await remove.mutateAsync(group.id);
      toast.show(`Group ${group.name} deleted`);
      await navigate({ to: '/o/$org/groups', params: { org } });
    } catch (err) {
      toast.show(err instanceof Error ? err.message : 'Deleting failed', 'destructive');
    }
  }

  return (
    <form onSubmit={save} className="flex max-w-[560px] flex-col gap-4">
      {stale && <StaleNotice org={org} />}
      <div className="flex flex-col gap-1">
        <Label htmlFor="settings-name">Name</Label>
        <Input id="settings-name" className="font-mono" required value={name} onChange={(e) => setName(e.target.value)} />
      </div>
      <div className="flex flex-col gap-1">
        <Label htmlFor="settings-description">Description</Label>
        <Input id="settings-description" value={description} onChange={(e) => setDescription(e.target.value)} />
      </div>
      <div className="flex flex-col gap-1">
        <Label htmlFor="settings-parent">Parent</Label>
        <select id="settings-parent" className={SELECT} value={parent} onChange={(e) => setParent(e.target.value)}>
          <option value="">— none (top level) —</option>
          {orderTree(groups)
            .filter(({ group: g }) => !excluded.has(g.id))
            .map(({ group: g, depth }) => (
              <option key={g.id} value={g.id}>
                {'  '.repeat(depth)}
                {g.name}
              </option>
            ))}
        </select>
      </div>
      <div className="flex flex-col gap-1">
        <Label htmlFor="settings-bastion">Jump host</Label>
        {bastions.data ? (
          <select id="settings-bastion" className={SELECT} value={bastion} onChange={(e) => setBastion(e.target.value)}>
            <option value="">— none (inherit from parent groups or the zone) —</option>
            {(bastions.data.bastions ?? []).map((b) => (
              <option key={b.id} value={b.id}>
                {b.name}
              </option>
            ))}
          </select>
        ) : (
          // No id here on purpose: the select above (and its options) is
          // what <Label htmlFor="settings-bastion"> names, so a caller
          // finding this field by label (getByLabelText) reliably waits
          // for the bastions list to be loaded rather than matching a
          // control whose options are not there yet.
          <div className={`${SELECT} flex items-center text-muted-foreground`}>Loading…</div>
        )}
        <span className="text-[11px] text-muted-foreground">Resolution: host → inventory group → zone default. Chains are rendered as ProxyJump.</span>
      </div>
      <div className="flex justify-between">
        <Button type="button" variant="destructive" onClick={() => void onDelete()}>
          Delete group
        </Button>
        <Button type="submit" variant="primary" disabled={update.isPending}>
          Save
        </Button>
      </div>
    </form>
  );
}
