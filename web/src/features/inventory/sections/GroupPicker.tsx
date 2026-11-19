import { Label } from '../../../ui/label.tsx';
import { Select } from '../../../ui/Select.tsx';
import { useGroups } from '../../../api/hooks/useInventory.ts';
import { useT } from '../../../i18n/useT.ts';

/** GroupPicker chooses an inventory group the host is not yet a direct
 * member of (`all` is no group of the inventory list, and is filtered out
 * should one ever be named so). The groups list is not paginated. */
export function GroupPicker({
  org, existing, value, onChange,
}: {
  org: string;
  existing: readonly string[];
  value: string;
  onChange: (groupId: string) => void;
}) {
  const t = useT();
  const groups = useGroups(org);
  const assignable = (groups.data?.groups ?? []).filter((g) => g.name !== 'all' && !existing.includes(g.id));
  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor="assign-group">{t.inventory.picker.group}</Label>
      <Select
        id="assign-group"
        required
        options={assignable.map((g) => ({ value: g.id, label: g.name }))}
        value={value}
        onChange={onChange}
        placeholder={t.inventory.picker.chooseGroup}
      />
      {!groups.data && groups.isError && (
        <span role="alert" className="text-[11px] text-destructive">
          {t.inventory.picker.groupsLoadFailed(groups.error instanceof Error ? groups.error.message : '')}
        </span>
      )}
      {groups.data && assignable.length === 0 && <span className="text-[11px] text-muted-foreground">{t.inventory.picker.allGroups}</span>}
    </div>
  );
}
