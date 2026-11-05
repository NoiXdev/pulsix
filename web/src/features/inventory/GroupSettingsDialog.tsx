import { useState, type FormEvent } from 'react';
import { Button } from '../../ui/button.tsx';
import { Dialog } from '../../ui/dialog.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { errorStatus } from '../../lib/apiError.ts';
import { useReloadInventory, useUpdateGroup } from '../../api/hooks/useInventory.ts';
import { useBastions } from '../../api/hooks/useSsh.ts';
import { useCredentials } from '../../api/hooks/useCredentials.ts';
import type { InventoryGroup, InventoryGroupSummary } from '../../api/types.ts';
import { descendantIds, orderTree } from './tree.ts';
import { StaleNotice } from './sections/StaleNotice.tsx';
import { BastionDialog } from '../credentials/BastionDialog.tsx';
import { useT } from '../../i18n/useT.ts';

const LOADING_FIELD = 'h-8 w-full rounded-md border border-border bg-background px-2 text-xs text-foreground';

/** GroupSettingsDialog edits a group's name, description, parent and jump
 * host ("Edit group" in the group page's sidebar). It closes on a save; a
 * 412 keeps it open with the stale notice, whose Reload starts the form
 * over from the stored group. The revision sent is the one the form was
 * filled from, never a newer one: saving old values on a fresh revision
 * would silently undo the other writer's change. */
export function GroupSettingsDialog({
  org, group, groups, canCreateCredential, onClose,
}: {
  org: string;
  group: InventoryGroup;
  groups: readonly InventoryGroupSummary[];
  /** Gates this dialog's own Jump host create row (POST /bastions is
   * credentials.write, not the inventory.write this dialog's own save needs —
   * see sshconf_handlers.go) and, forwarded, BastionDialog's own nested
   * Credential create row (v0.4g Task 7 and 10 fix rounds). */
  canCreateCredential: boolean;
  onClose: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const bastions = useBastions(org);
  const credentials = useCredentials(org);
  const update = useUpdateGroup(org, group.id);
  const reload = useReloadInventory(org);
  const [revision, setRevision] = useState(group.revision);
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

  function fillFrom(g: InventoryGroup) {
    setName(g.name);
    setDescription(g.description);
    setParent(g.parent_id ?? '');
    setBastion(g.bastion_id ?? '');
    setRevision(g.revision);
  }

  async function onReload() {
    const fresh = await reload(group.id);
    if (fresh) fillFrom(fresh);
    setStale(false);
  }

  async function save(e: FormEvent) {
    e.preventDefault();
    try {
      await update.mutateAsync({ name, description: description || undefined, parent_id: parent || undefined, bastion_id: bastion || undefined, revision });
      toast.show(t.inventory.settings.saved);
      onClose();
    } catch (err) {
      if (errorStatus(err) === 412) return setStale(true);
      toast.show(err instanceof Error ? err.message : t.inventory.settings.saveFailed, 'destructive');
    }
  }

  return (
    // The create-row dialog renders outside this <form>, not inside it:
    // BastionDialog's own <form> would otherwise be a DOM descendant of
    // this one, and a native "submit" event bubbles through <dialog> just
    // like anything else — clicking "Create jump host" would fire this
    // form's own onSubmit too.
    <Dialog open onClose={onClose} title={t.inventory.settings.editTitle}>
      <form onSubmit={save} className="flex flex-col gap-4">
        {stale && <StaleNotice onReload={onReload} />}
        <div className="flex flex-col gap-1">
          <Label htmlFor="settings-name">{t.inventory.settings.name}</Label>
          <Input id="settings-name" className="font-mono" required value={name} onChange={(e) => setName(e.target.value)} />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="settings-description">{t.inventory.settings.description}</Label>
          <Input id="settings-description" value={description} onChange={(e) => setDescription(e.target.value)} />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="settings-parent">{t.inventory.settings.parent}</Label>
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
            placeholder={t.inventory.settings.parentNone}
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="settings-bastion">{t.inventory.settings.jumpHost}</Label>
          {bastions.data ? (
            <Select
              id="settings-bastion"
              options={(bastions.data.bastions ?? []).map((b) => ({ value: b.id, label: b.name }))}
              value={bastion}
              onChange={setBastion}
              placeholder={t.inventory.settings.jumpHostNone}
              create={canCreateCredential ? { label: t.inventory.settings.newJumpHost, onSelect: setNewBastionName } : undefined}
            />
          ) : (
            // No id here on purpose: the select above (and its options) is
            // what <Label htmlFor="settings-bastion"> names, so a caller
            // finding this field by label (getByLabelText) reliably waits
            // for the bastions list to be loaded rather than matching a
            // control whose options are not there yet.
            <div className={`${LOADING_FIELD} flex items-center text-muted-foreground`}>{t.common.loading}</div>
          )}
          <span className="text-[11px] text-muted-foreground">{t.inventory.settings.resolution}</span>
        </div>
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>
            {t.common.cancel}
          </Button>
          <Button type="submit" variant="primary" disabled={update.isPending}>
            {t.common.save}
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
    </Dialog>
  );
}
