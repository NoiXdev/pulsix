import { useState, type ReactNode } from 'react';
import { useNavigate } from '@tanstack/react-router';
import type { HostListItem } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import type { RowAction } from '../../ui/data-list/index.ts';
import { AdhocDialog } from '../runs/AdhocDialog.tsx';
import { RunTemplateDialog } from '../runs/RunTemplateDialog.tsx';
import { liveRunsOf } from '../runs/runsSearch.ts';
import { HostDialog } from './HostDialog.tsx';
import { hostRowActions, useHostCommands, type HostRowHandlers } from './hostActions.ts';

type OpenDialog = { kind: 'edit' | 'run' | 'template'; host: HostListItem };

/**
 * useHostListActions is the hosts list's row menu for every row at once:
 * `rowActions(host)` builds a host's entries (DataList shows them in the
 * "…" menu and on a right-click), and `dialogs` holds the one dialog an
 * entry opened, for the host it was picked on. Render `dialogs` next to the
 * list.
 */
export function useHostListActions(org: string, role: string | undefined): { rowActions: (host: HostListItem) => RowAction[]; dialogs: ReactNode } {
  const t = useT();
  const navigate = useNavigate();
  const commands = useHostCommands(org);
  const [dialog, setDialog] = useState<OpenDialog | null>(null);
  const close = () => setDialog(null);

  const on: HostRowHandlers = {
    open: (host) => void navigate({ to: '/o/$org/hosts/$id', params: { org, id: host.id } }),
    edit: (host) => setDialog({ kind: 'edit', host }),
    runCommand: (host) => setDialog({ kind: 'run', host }),
    runTemplate: (host) => setDialog({ kind: 'template', host }),
    viewActiveRuns: (host) => void navigate({ to: '/o/$org/runs', params: { org }, search: liveRunsOf(host.id) }),
    switchCapabilities: (host, patch) => commands.switchCapabilities(host, patch),
    remove: (host) => void commands.remove(host),
  };

  return {
    rowActions: (host) => hostRowActions(host, role, on, t),
    dialogs: dialog && (
      <>
        {dialog.kind === 'edit' && <HostDialog org={org} role={role} open onOpenChange={close} host={dialog.host} />}
        {dialog.kind === 'run' && <AdhocDialog org={org} onClose={close} initialHostIds={[dialog.host.id]} />}
        {dialog.kind === 'template' && <RunTemplateDialog org={org} host={dialog.host} onClose={close} />}
      </>
    ),
  };
}
