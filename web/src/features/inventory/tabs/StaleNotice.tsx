import { useQueryClient } from '@tanstack/react-query';
import { Button } from '../../../ui/button.tsx';

/** StaleNotice answers a 412: someone else saved this group first. */
export function StaleNotice({ org }: { org: string }) {
  const qc = useQueryClient();
  return (
    <div role="alert" className="flex items-center justify-between rounded-md border border-border bg-card px-4 py-3 text-xs">
      <span>This group changed since you loaded it. Reload to see the current version, then apply your change again.</span>
      <Button onClick={() => void qc.invalidateQueries({ queryKey: ['inventory', org] })}>Reload</Button>
    </div>
  );
}
