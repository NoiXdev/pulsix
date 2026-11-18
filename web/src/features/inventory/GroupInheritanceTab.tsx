import { DisabledHint } from '../../ui/detail-layout.tsx';
import type { InventoryGroup } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';

/** GroupInheritanceTab will show, per variable, every level on the group's
 * path from the schema default down to the group (or one of its hosts) and
 * which one wins (spec "Group page", point 5). Until then it says so. */
export function GroupInheritanceTab({ org, group }: { org: string; group: InventoryGroup }) {
  const t = useT();
  void org;
  void group;
  return <DisabledHint title={t.inventory.groupPage.inheritance.title} body={t.inventory.groupPage.inheritance.coming} />;
}
