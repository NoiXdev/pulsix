import { useState, type FormEvent } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useCreateGroup } from '../../api/hooks/useInventory.ts';
import type { InventoryGroupSummary } from '../../api/types.ts';
import { orderTree } from './tree.ts';
import { useT } from '../../i18n/useT.ts';

export function CreateGroupDialog({
  org, open, onClose, groups, fixedParent, onCreated,
}: {
  org: string;
  open: boolean;
  onClose: () => void;
  groups: readonly InventoryGroupSummary[];
  fixedParent?: string;
  onCreated: (id: string) => void;
}) {
  const t = useT();
  const toast = useToast();
  const create = useCreateGroup(org);
  const [name, setName] = useState('');
  const [parent, setParent] = useState(fixedParent ?? '');
  const [description, setDescription] = useState('');
  const [error, setError] = useState<string | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    try {
      const res = await create.mutateAsync({ name, description: description || undefined, parent_id: parent || undefined });
      toast.show(t.inventory.create.created(res.group.name));
      setName('');
      setDescription('');
      onCreated(res.group.id);
    } catch (err) {
      setError(err instanceof Error ? err.message : t.inventory.create.failed);
    }
  }

  return (
    <Dialog open={open} onClose={onClose} title={fixedParent ? t.inventory.create.titleChild : t.inventory.create.titleGroup}>
      <form onSubmit={submit} className="flex flex-col gap-4">
        <div className="flex flex-col gap-1">
          <Label htmlFor="group-name">{t.inventory.create.name}</Label>
          <Input id="group-name" className="font-mono" required value={name} onChange={(e) => setName(e.target.value)} />
          <span className="text-[11px] text-muted-foreground">{t.inventory.create.nameHint}</span>
        </div>
        {!fixedParent && (
          <div className="flex flex-col gap-1">
            <Label htmlFor="group-parent">{t.inventory.create.parent}</Label>
            <Select
              id="group-parent"
              // The hint names each row's own parent, so two groups sharing
              // a name in different branches of the tree stay tellable
              // apart; the indentation (inside the label, not a separate
              // column) is what still reads as a tree once the combobox
              // collapses the list to one line.
              options={orderTree(groups).map(({ group, depth }) => ({
                value: group.id,
                label: `${'  '.repeat(depth)}${group.name}`,
                hint: groups.find((g) => g.id === group.parent_id)?.name,
              }))}
              value={parent}
              onChange={setParent}
              placeholder={t.inventory.create.parentNone}
            />
          </div>
        )}
        <div className="flex flex-col gap-1">
          <Label htmlFor="group-description">{t.inventory.create.description}</Label>
          <Input id="group-description" value={description} onChange={(e) => setDescription(e.target.value)} />
        </div>
        {error && (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>
            {t.common.cancel}
          </Button>
          <Button type="submit" variant="primary" disabled={create.isPending}>
            {t.inventory.create.submit}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
