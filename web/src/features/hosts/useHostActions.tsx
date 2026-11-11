import { useState, type ReactNode } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { useDeleteHost, useSetHostCapabilities } from '../../api/hooks/useHosts.ts';
import type { Host } from '../../api/types.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import { useToast } from '../../ui/toastContext.ts';
import { AdhocDialog } from '../runs/AdhocDialog.tsx';
import { RunTemplateDialog } from '../runs/RunTemplateDialog.tsx';
import { HostDialog } from './HostDialog.tsx';
import { useConfirm } from '../../ui/confirmContext.ts';

export interface HostActions {
  /** What the caller's role allows; an action it may not use is not offered. */
  canEdit: boolean;
  canRun: boolean;
  canSwitch: boolean;
  canDelete: boolean;
  open: () => void;
  edit: () => void;
  runCommand: () => void;
  runTemplate: () => void;
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
 * per row. The caller passes the caller's `role` (undefined while it is
 * still loading, which offers nothing): it already holds the org, and a
 * second read here would repeat that request. `onDeleted` lets a caller that sits on the host's own page leave
 * it once the host is gone.
 */
export function useHostActions(org: string, host: Host, role: string | undefined, opts: { onDeleted?: () => void } = {}): HostActions {
  const t = useT();
  const toast = useToast();
  const ask = useConfirm();
  const navigate = useNavigate();
  const setCapabilities = useSetHostCapabilities(org, host.id);
  const deleteHost = useDeleteHost(org);
  const [dialog, setDialog] = useState<'edit' | 'run' | 'template' | null>(null);

  const canWrite = may(role, 'hosts.write');

  function patch(change: { managed?: boolean; monitored?: boolean }) {
    setCapabilities.mutate(change, {
      onError: (err) => toast.show(err instanceof Error ? err.message : t.hosts.row.updateFailed, 'destructive'),
    });
  }

  async function remove() {
    if (!(await ask({ message: t.hosts.detail.deleteConfirm(host.name), confirmLabel: t.common.delete, destructive: true }))) return;
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
    runCommand: () => setDialog('run'),
    runTemplate: () => setDialog('template'),
    toggleMonitoring: () => patch({ monitored: !host.monitored }),
    toggleManagement: () => patch({ managed: !host.managed }),
    remove: () => void remove(),
    dialogs: (
      <>
        {dialog === 'edit' && <HostDialog org={org} role={role} open onOpenChange={() => setDialog(null)} host={host} />}
        {dialog === 'run' && <AdhocDialog org={org} onClose={() => setDialog(null)} initialHostIds={[host.id]} />}
        {dialog === 'template' && <RunTemplateDialog org={org} host={host} onClose={() => setDialog(null)} />}
      </>
    ),
  };
}
