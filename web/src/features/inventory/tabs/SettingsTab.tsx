import { useState, type FormEvent } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { Button } from '../../../ui/button.tsx';
import { Input } from '../../../ui/input.tsx';
import { Label } from '../../../ui/label.tsx';
import { Select } from '../../../ui/Select.tsx';
import { useToast } from '../../../ui/toastContext.ts';
import { errorStatus } from '../../../lib/apiError.ts';
import { useDeleteGroup, useUpdateGroup } from '../../../api/hooks/useInventory.ts';
import { useBastions } from '../../../api/hooks/useSsh.ts';
import { useCredentials } from '../../../api/hooks/useCredentials.ts';
import type { InventoryGroup, InventoryGroupSummary } from '../../../api/types.ts';
import { descendantIds, orderTree } from '../tree.ts';
import { StaleNotice } from './StaleNotice.tsx';
import { BastionDialog } from '../../credentials/BastionDialog.tsx';

const LOADING_FIELD = 'h-8 w-full rounded-md border border-border bg-background px-2 text-xs text-foreground';

export function SettingsTab({
  org, group, groups, canCreateCredential,
}: {
  org: string;
  group: InventoryGroup;
  groups: readonly InventoryGroupSummary[];
  /** Gates this tab's own Jump host create row (POST /bastions is
   * credentials.write, not the inventory.write this tab's own save needs —
   * see sshconf_handlers.go) and, forwarded, BastionDialog's own nested
   * Credential create row (v0.4g Task 7 and 10 fix rounds). */
  canCreateCredential: boolean;
}) {
  const toast = useToast();
  const navigate = useNavigate();
  const bastions = useBastions(org);
  const credentials = useCredentials(org);
  const update = useUpdateGroup(org, group.id);
  const remove = useDeleteGroup(org);
  const [name, setName] = useState(group.name);
  const [description, setDescription] = useState(group.description);
  const [parent, setParent] = useState(group.parent_id ?? '');
  const [bastion, setBastion] = useState(group.bastion_id ?? '');
  const [stale, setStale] = useState(false);
  // Non-null while the "New jump host…" row's dialog is open; its value is
  // whatever had been typed into the picker, so BastionDialog can prefill
  // the Name field with it (null, not '', is "closed").
  const [newBastionName, setNewBastionName] = useState<string | null>(null);
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
    // The create-row dialog renders outside this <form>, not inside it:
    // BastionDialog's own <form> would otherwise be a DOM descendant of
    // this one, and a native "submit" event bubbles through <dialog> just
    // like anything else — clicking "Create jump host" would fire this
    // form's own onSubmit too.
    <>
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
          <Select
            id="settings-parent"
            // The hint names each row's own parent, so two groups sharing a
            // name in different branches of the tree stay tellable apart.
            options={orderTree(groups)
              .filter(({ group: g }) => !excluded.has(g.id))
              .map(({ group: g, depth }) => ({
                value: g.id,
                label: `${'  '.repeat(depth)}${g.name}`,
                hint: groups.find((p) => p.id === g.parent_id)?.name,
              }))}
            value={parent}
            onChange={setParent}
            placeholder="— none (top level) —"
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="settings-bastion">Jump host</Label>
          {bastions.data ? (
            <Select
              id="settings-bastion"
              options={(bastions.data.bastions ?? []).map((b) => ({ value: b.id, label: b.name }))}
              value={bastion}
              onChange={setBastion}
              placeholder="— none (inherit from parent groups or the zone) —"
              create={canCreateCredential ? { label: 'New jump host…', onSelect: setNewBastionName } : undefined}
            />
          ) : (
            // No id here on purpose: the select above (and its options) is
            // what <Label htmlFor="settings-bastion"> names, so a caller
            // finding this field by label (getByLabelText) reliably waits
            // for the bastions list to be loaded rather than matching a
            // control whose options are not there yet.
            <div className={`${LOADING_FIELD} flex items-center text-muted-foreground`}>Loading…</div>
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
      {newBastionName !== null && (
        <BastionDialog
          org={org}
          bastions={bastions.data?.bastions ?? []}
          credentials={credentials.data?.credentials ?? []}
          canCreateCredential={canCreateCredential}
          initialName={newBastionName}
          onClose={() => setNewBastionName(null)}
          onCreated={(id) => {
            setBastion(id);
            setNewBastionName(null);
          }}
        />
      )}
    </>
  );
}
