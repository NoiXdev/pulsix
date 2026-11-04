import { useState, type ReactNode } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { useDeleteHost, useSetHostCapabilities } from '../../api/hooks/useHosts.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import type { Host } from '../../api/types.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import { useToast } from '../../ui/toastContext.ts';
import { AdhocDialog } from '../runs/AdhocDialog.tsx';
import { HostDialog } from './HostDialog.tsx';

export interface HostActions {
  /** What the caller's role allows; an action it may not use is not offered. */
  canEdit: boolean;
  canRun: boolean;
  canSwitch: boolean;
  canDelete: boolean;
  open: () => void;
  edit: () => void;
  runPlay: () => void;
  toggleMonitoring: () => void;
  toggleManagement: () => void;
  remove: () => void;
  /** The dialogs the actions open. Render them next to, never inside, a
   * link: a native <dialog> is not portalled, so a click in one would
   * bubble to the row's anchor. */
  dialogs: ReactNode;
}

/**
 * useHostActions is the host's action set shared by the list's row menu and
 * the host page's menu: what a role may do, and what each action does. The
 * dialogs are mounted only while open, so a long list does not carry a form
 * per row. `onDeleted` lets a caller that sits on the host's own page leave
 * it once the host is gone.
 */
export function useHostActions(org: string, host: Host, opts: { onDeleted?: () => void } = {}): HostActions {
  const t = useT();
  const toast = useToast();
  const navigate = useNavigate();
  const role = useOrg(org).data?.org.role;
  const setCapabilities = useSetHostCapabilities(org, host.id);
  const deleteHost = useDeleteHost(org);
  const [dialog, setDialog] = useState<'edit' | 'run' | null>(null);

  const canWrite = may(role, 'hosts.write');

  function patch(change: { managed?: boolean; monitored?: boolean }) {
    setCapabilities.mutate(change, {
      onError: (err) => toast.show(err instanceof Error ? err.message : t.hosts.row.updateFailed, 'destructive'),
    });
  }

  async function remove() {
    if (!confirm(t.hosts.detail.deleteConfirm(host.name))) return;
    try {
      await deleteHost.mutateAsync(host.id);
      toast.show(t.hosts.detail.deleted(host.name));
      opts.onDeleted?.();
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.hosts.detail.deleteFailed, 'destructive');
    }
  }

  return {
    canEdit: canWrite,
    canRun: host.managed && may(role, 'jobs.run'),
    canSwitch: canWrite,
    canDelete: canWrite,
    open: () => void navigate({ to: '/o/$org/hosts/$id', params: { org, id: host.id } }),
    edit: () => setDialog('edit'),
    runPlay: () => setDialog('run'),
    toggleMonitoring: () => patch({ monitored: !host.monitored }),
    toggleManagement: () => patch({ managed: !host.managed }),
    remove: () => void remove(),
    dialogs: (
      <>
        {dialog === 'edit' && <HostDialog org={org} open onOpenChange={() => setDialog(null)} host={host} />}
        {dialog === 'run' && <AdhocDialog org={org} onClose={() => setDialog(null)} initialHostIds={[host.id]} />}
      </>
    ),
  };
}
