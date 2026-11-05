import { Button } from '../../../ui/button.tsx';
import { Pill } from '../../../ui/badge.tsx';
import { useQueryGuard } from '../../../ui/useQueryGuard.tsx';
import { QueryLoading } from '../../../ui/QueryState.tsx';
import { useInventoryPreview } from '../../../api/hooks/useInventory.ts';
import type { InventoryGroup } from '../../../api/types.ts';
import { useT } from '../../../i18n/useT.ts';

export function PreviewSection({ org, group }: { org: string; group: InventoryGroup }) {
  const t = useT();
  const preview = useInventoryPreview(org, group.id);
  const guard = useQueryGuard(preview);
  if (guard) return guard;
  if (!preview.data) return <QueryLoading />;
  const json = JSON.stringify(preview.data.inventory, null, 2);
  const warnings = preview.data.warnings ?? [];
  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center justify-between gap-4">
        <p className="text-xs text-muted-foreground">
          {t.inventory.preview.introBefore}
          <span className="font-mono">{group.name}</span>
          {t.inventory.preview.introAfter}
        </p>
        <Button onClick={() => void navigator.clipboard.writeText(json)}>{t.inventory.preview.copy}</Button>
      </div>
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
      <pre aria-label={t.inventory.preview.json} className="max-h-[560px] overflow-auto rounded-[10px] border border-border bg-background p-4 font-mono text-[12px] leading-relaxed">
        {json}
      </pre>
    </div>
  );
}
