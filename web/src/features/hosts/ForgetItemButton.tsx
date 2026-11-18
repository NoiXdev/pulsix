import { Eraser } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { useConfirm } from '../../ui/confirmContext.ts';
import { useToast } from '../../ui/toastContext.ts';
import { useForgetItem } from '../../api/hooks/useItems.ts';
import { useT } from '../../i18n/useT.ts';

export interface ForgetItemButtonProps {
  org: string;
  hostId: string;
  /** The check the item belongs to — forgetting names `(check_key, item)`
   * together, like an item decision. */
  checkKey: string;
  /** The item to forget, every metric of it. */
  item: string;
  /** Forget only this metric of the item — a vanished row (backlog 3.88);
   * left out, the whole item. */
  metric?: string;
}

/**
 * ForgetItemButton is a stale item row's **Forget** action (backlog 3.12b,
 * spec "Manual forget"): a container, mount or unit that is gone for good
 * is removed from the host at once instead of waiting for the automatic
 * expiry — its current values, rule states and open alerts (closed as
 * forgotten). Its graphs stay; if it reports again it comes back.
 *
 * It asks first: the values and alerts it removes do not come back on
 * their own unless the item reports again. Which rows offer it is
 * HostCheckValueRow's call (`offersForget`), not this button's.
 */
export function ForgetItemButton({ org, hostId, checkKey, item, metric }: ForgetItemButtonProps) {
  const t = useT();
  const what = metric === undefined ? item : item ? `${item} ${metric}` : metric;
  const confirm = useConfirm();
  const toast = useToast();
  const forget = useForgetItem(org, hostId);

  async function onClick() {
    const ok = await confirm({
      title: t.hosts.forget.confirmTitle(what),
      message: metric === undefined ? t.hosts.forget.confirmBody : t.hosts.forget.confirmBodyMetric,
      confirmLabel: t.hosts.forget.forget,
      destructive: true,
    });
    if (!ok) return;
    try {
      await forget.mutateAsync({ checkKey, item, metric });
      toast.show(t.hosts.forget.done(what));
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.hosts.forget.failed, 'destructive');
    }
  }

  return (
    <Button
      type="button"
      variant="ghost"
      aria-label={t.hosts.forget.label(checkKey, what)}
      disabled={forget.isPending}
      onClick={() => void onClick()}
    >
      <Eraser size={14} /> {t.hosts.forget.forget}
    </Button>
  );
}
