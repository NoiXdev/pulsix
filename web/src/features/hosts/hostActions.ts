import { useDeleteHost, useSetCapabilitiesOfHost, type CapabilityPatch } from '../../api/hooks/useHosts.ts';
import type { Host, HostListItem } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import { useT } from '../../i18n/useT.ts';
import { may } from '../../lib/roles.ts';
import { useConfirm } from '../../ui/confirmContext.ts';
import type { RowAction } from '../../ui/data-list/index.ts';
import { useToast } from '../../ui/toastContext.ts';

/** What the caller's role may do with a host; an action it may not use is
 * not offered. Running a command or a template needs a managed host. */
export interface HostPermissions {
  canEdit: boolean;
  canRun: boolean;
  canSwitch: boolean;
  canDelete: boolean;
}

/** hostPermissions is the one place both host menus (the list's row menu
 * and the host page's) read what a role may do. `role` is undefined while
 * it loads, which allows nothing. */
export function hostPermissions(host: Pick<Host, 'managed'>, role: string | undefined): HostPermissions {
  const canWrite = may(role, 'hosts.write');
  return { canEdit: canWrite, canRun: host.managed && may(role, 'jobs.run'), canSwitch: canWrite, canDelete: canWrite };
}

/** What each entry of a host's row menu does, for the host it was picked on. */
export interface HostRowHandlers {
  open: (host: HostListItem) => void;
  edit: (host: HostListItem) => void;
  runCommand: (host: HostListItem) => void;
  runTemplate: (host: HostListItem) => void;
  viewActiveRuns: (host: HostListItem) => void;
  switchCapabilities: (host: HostListItem, patch: CapabilityPatch) => void;
  remove: (host: HostListItem) => void;
}

/**
 * hostRowActions is a host's row menu in the hosts list (the "…" button and
 * the right-click menu): Open, then what the role may do, in the order the
 * menu has always had. A host with live runs also offers them.
 */
export function hostRowActions(host: HostListItem, role: string | undefined, on: HostRowHandlers, t: Messages): RowAction[] {
  const can = hostPermissions(host, role);
  const r = t.hosts.row;
  const items: RowAction[] = [{ id: 'open', label: r.open, onSelect: () => on.open(host) }];
  if (can.canEdit) items.push({ id: 'edit', label: r.edit, onSelect: () => on.edit(host) });
  if (can.canRun) {
    items.push({ id: 'run-command', label: r.runCommand, onSelect: () => on.runCommand(host) });
    items.push({ id: 'run-template', label: r.runTemplate, onSelect: () => on.runTemplate(host) });
  }
  if (host.active_runs > 0) {
    items.push({ id: 'active-runs', label: r.viewActiveRuns(host.active_runs), onSelect: () => on.viewActiveRuns(host) });
  }
  if (can.canSwitch) {
    items.push({
      id: 'monitoring',
      label: host.monitored ? r.monitoringOff : r.monitoringOn,
      onSelect: () => on.switchCapabilities(host, { monitored: !host.monitored }),
    });
    items.push({
      id: 'management',
      label: host.managed ? r.managementOff : r.managementOn,
      onSelect: () => on.switchCapabilities(host, { managed: !host.managed }),
    });
  }
  if (can.canDelete) items.push({ id: 'delete', label: r.delete, onSelect: () => on.remove(host), destructive: true });
  return items;
}

/**
 * useHostCommands is what the host menus do without a dialog: switch a
 * capability (a failure says why in a toast) and delete a host after asking.
 * `remove` resolves once the host is gone or the person declined.
 */
export function useHostCommands(org: string) {
  const t = useT();
  const toast = useToast();
  const ask = useConfirm();
  const setCapabilities = useSetCapabilitiesOfHost(org);
  const deleteHost = useDeleteHost(org);

  function switchCapabilities(host: Pick<Host, 'id'>, patch: CapabilityPatch) {
    setCapabilities.mutate(
      { id: host.id, patch },
      { onError: (err) => toast.show(err instanceof Error ? err.message : t.hosts.row.updateFailed, 'destructive') },
    );
  }

  async function remove(host: Pick<Host, 'id' | 'name'>, onDeleted?: () => void) {
    if (!(await ask({ message: t.hosts.detail.deleteConfirm(host.name), confirmLabel: t.common.delete, destructive: true }))) return;
    try {
      await deleteHost.mutateAsync(host.id);
      toast.show(t.hosts.detail.deleted(host.name));
      onDeleted?.();
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.hosts.detail.deleteFailed, 'destructive');
    }
  }

  return { switchCapabilities, remove };
}
