import { useState, type ReactNode } from 'react';
import { useNavigate } from '@tanstack/react-router';
import type { Host } from '../../api/types.ts';
import { AdhocDialog } from '../runs/AdhocDialog.tsx';
import { RunTemplateDialog } from '../runs/RunTemplateDialog.tsx';
import { HostDialog } from './HostDialog.tsx';
import { hostPermissions, useHostCommands, type HostPermissions } from './hostActions.ts';

export interface HostActions extends HostPermissions {
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
 * useHostActions is one host's action set for the host page's menu: what a
 * role may do (hostPermissions, shared with the hosts list's row menu), and
 * what each action does. The dialogs are mounted only while open. The
 * caller passes the caller's `role` (undefined while it is still loading,
 * which offers nothing): it already holds the org, and a second read here
 * would repeat that request. `onDeleted` lets a caller that sits on the
 * host's own page leave it once the host is gone.
 */
export function useHostActions(org: string, host: Host, role: string | undefined, opts: { onDeleted?: () => void } = {}): HostActions {
  const navigate = useNavigate();
  const commands = useHostCommands(org);
  const [dialog, setDialog] = useState<'edit' | 'run' | 'template' | null>(null);

  return {
    ...hostPermissions(host, role),
    open: () => void navigate({ to: '/o/$org/hosts/$id', params: { org, id: host.id } }),
    edit: () => setDialog('edit'),
    runCommand: () => setDialog('run'),
    runTemplate: () => setDialog('template'),
    toggleMonitoring: () => commands.switchCapabilities(host, { monitored: !host.monitored }),
    toggleManagement: () => commands.switchCapabilities(host, { managed: !host.managed }),
    remove: () => void commands.remove(host, opts.onDeleted),
    dialogs: (
      <>
        {dialog === 'edit' && <HostDialog org={org} role={role} open onOpenChange={() => setDialog(null)} host={host} />}
        {dialog === 'run' && <AdhocDialog org={org} onClose={() => setDialog(null)} initialHostIds={[host.id]} />}
        {dialog === 'template' && <RunTemplateDialog org={org} host={host} onClose={() => setDialog(null)} />}
      </>
    ),
  };
}
