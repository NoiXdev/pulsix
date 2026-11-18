import { Pill } from '../../../ui/badge.tsx';
import { JsonViewer } from '../../../ui/json-viewer.tsx';
import { useQueryGuard } from '../../../ui/useQueryGuard.tsx';
import { QueryLoading } from '../../../ui/QueryState.tsx';
import { useInventoryPreview } from '../../../api/hooks/useInventory.ts';
import type { InventoryGroup } from '../../../api/types.ts';
import { useT } from '../../../i18n/useT.ts';

/** The renderer's stand-in for every secret in a preview
 * (internal/inventory/render.MaskedSecret). */
const MASKED = '********';

/** PreviewSection is the Inventory tab: the inventory a job targeting the
 * group receives, secrets masked, in the JSON viewer, with the renderer's
 * warnings above it. The hosts' vars start folded: there is one block per
 * host, and the groups are what this page is about. */
export function PreviewSection({ org, group }: { org: string; group: InventoryGroup }) {
  const t = useT();
  const preview = useInventoryPreview(org, group.id);
  const guard = useQueryGuard(preview);
  if (guard) return guard;
  if (!preview.data) return <QueryLoading />;
  const warnings = preview.data.warnings ?? [];
  return (
    <div className="flex flex-col gap-3">
      <p className="text-xs text-muted-foreground">
        {t.inventory.preview.introBefore}
        <span className="font-mono">{group.name}</span>
        {t.inventory.preview.introAfter}
      </p>
      {warnings.length > 0 && (
        <ul className="flex flex-col gap-1.5" aria-label={t.inventory.preview.warnings}>
          {warnings.map((w, i) => (
            <li key={i} className="flex items-center gap-2 text-xs">
              <Pill tone="warn">{t.inventory.preview.warning}</Pill>
              {w.host && <span className="font-mono">{w.host}</span>}
              <span>{w.message}</span>
            </li>
          ))}
        </ul>
      )}
      <JsonViewer value={preview.data.inventory} label={t.inventory.preview.json} isMasked={(v) => v === MASKED} initiallyCollapsed={['_meta.hostvars']} />
    </div>
  );
}
