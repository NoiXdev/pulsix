import { useState, type FormEvent } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useCreateGroup } from '../../api/hooks/useInventory.ts';
import type { InventoryGroupSummary } from '../../api/types.ts';
import { orderTree } from './tree.ts';

const SELECT = 'h-8 w-full rounded-md border border-border bg-background px-2 text-xs text-foreground';

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
      toast.show(`Group ${res.group.name} created`);
      setName('');
      setDescription('');
      onCreated(res.group.id);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Creating the group failed');
    }
  }

  return (
    <Dialog open={open} onClose={onClose} title={fixedParent ? 'Add child group' : 'Add group'}>
      <form onSubmit={submit} className="flex flex-col gap-4">
        <div className="flex flex-col gap-1">
          <Label htmlFor="group-name">Name</Label>
          <Input id="group-name" className="font-mono" required value={name} onChange={(e) => setName(e.target.value)} />
          <span className="text-[11px] text-muted-foreground">Lowercase letters, digits and underscores; this becomes the Ansible group name.</span>
        </div>
        {!fixedParent && (
          <div className="flex flex-col gap-1">
            <Label htmlFor="group-parent">Parent</Label>
            <select id="group-parent" className={SELECT} value={parent} onChange={(e) => setParent(e.target.value)}>
              <option value="">— none (top level) —</option>
              {orderTree(groups).map(({ group, depth }) => (
                <option key={group.id} value={group.id}>
                  {'  '.repeat(depth)}
                  {group.name}
                </option>
              ))}
            </select>
          </div>
        )}
        <div className="flex flex-col gap-1">
          <Label htmlFor="group-description">Description</Label>
          <Input id="group-description" value={description} onChange={(e) => setDescription(e.target.value)} />
        </div>
        {error && (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={create.isPending}>
            Create group
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
