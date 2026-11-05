import { useQueryClient } from '@tanstack/react-query';
import { Button } from '../../../ui/button.tsx';
import { useT } from '../../../i18n/useT.ts';

/** StaleNotice answers a 412: someone else saved this group first. */
export function StaleNotice({ org }: { org: string }) {
  const t = useT();
  const qc = useQueryClient();
  return (
    <div role="alert" className="flex items-center justify-between rounded-md border border-border bg-card px-4 py-3 text-xs">
      <span>{t.inventory.stale.message}</span>
      <Button onClick={() => void qc.invalidateQueries({ queryKey: ['inventory', org] })}>{t.inventory.stale.reload}</Button>
    </div>
  );
}
