import { useId } from 'react';
import type { Template } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { HostChecklist } from '../runs/HostChecklist.tsx';
import type { TemplateHosts } from './useTemplateHosts.ts';

/**
 * DryRunHosts chooses the hosts a dry run narrows to (none: all of the
 * template's). It offers the template's own hosts where its scope resolves
 * in the browser (useTemplateHosts), else every managed host — the server
 * refuses one outside the template either way.
 */
export function DryRunHosts({ org, template, scope, selected, onToggle }: { org: string; template: Template | undefined; scope: TemplateHosts; selected: readonly string[]; onToggle: (id: string) => void }) {
  const t = useT();
  const d = t.playbooks.page.dryRun;
  const labelId = useId();
  if (!template) return null;
  if (scope.kind === 'open') {
    return (
      <div className="flex flex-col gap-1">
        <HostChecklist org={org} id="dry-run-hosts" label={d.hosts} selected={selected} onToggle={onToggle} />
        <span className="text-[11px] text-muted-foreground">{d.hostsHint}</span>
      </div>
    );
  }
  return (
    <div className="flex flex-col gap-1">
      <span id={labelId} className="text-xs font-medium">
        {d.hosts}
      </span>
      <div role="group" aria-labelledby={labelId} className="flex max-h-48 flex-col gap-1 overflow-y-auto rounded-md border border-border p-2">
        {scope.kind === 'pending' ? (
          <span className="text-[11px] text-muted-foreground">{d.hostsReading}</span>
        ) : scope.hosts.length === 0 ? (
          <span className="text-[11px] text-muted-foreground">{d.hostsNone}</span>
        ) : (
          scope.hosts.map((h) => (
            <label key={h.id} className="flex items-center gap-2 text-xs">
              <input type="checkbox" checked={selected.includes(h.id)} onChange={() => onToggle(h.id)} aria-label={h.name} />
              {h.name}
            </label>
          ))
        )}
      </div>
      <span className="text-[11px] text-muted-foreground">{d.hostsScopedHint}</span>
    </div>
  );
}
