import { useId, useState } from 'react';
import { Pill } from '../../ui/badge.tsx';
import { Button } from '../../ui/button.tsx';
import { Select } from '../../ui/Select.tsx';
import { Card } from '../../ui/table.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { toVarSchema, useInventoryPreview } from '../../api/hooks/useInventory.ts';
import type { InventoryGroup } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import { useT } from '../../i18n/useT.ts';
import { formatValue } from './provenance.ts';
import { groupHosts, groupInheritance, hostInheritance, inventoryHas, type Cell, type GroupSource, type Inheritance, type Level } from './inheritance.ts';

type M = Messages['inventory']['inheritance'];

/**
 * GroupInheritanceTab shows, per variable, every level on the group's path
 * from the weakest to the strongest and which one wins (spec "Group page",
 * point 5) — for the group, or for one of its hosts together with the
 * host's other groups and its own values. It reads the group's inventory
 * preview, the server's rendering, so the table follows what a job
 * receives (inheritance.ts says how); a group the scoped preview does not
 * render (no managed host at or below it) is read from the organization's.
 */
export function GroupInheritanceTab({ org, group }: { org: string; group: InventoryGroup }) {
  const t = useT();
  const viewLabel = useId();
  const scoped = useInventoryPreview(org, group.id);
  const needWhole = !!scoped.data && !inventoryHas(scoped.data.inventory, group.name);
  const whole = useInventoryPreview(org, undefined, { enabled: needWhole });
  const [view, setView] = useState<'group' | 'host'>('group');
  const [picked, setPicked] = useState('');

  const scopedGuard = useQueryGuard(scoped);
  const wholeGuard = useQueryGuard(whole);
  if (scopedGuard) return scopedGuard;
  if (!scoped.data) return <QueryLoading />;
  if (needWhole && wholeGuard) return wholeGuard;
  const doc = needWhole ? whole.data?.inventory : scoped.data.inventory;
  if (!doc) return <QueryLoading />;

  const m = t.inventory.inheritance;
  if (!inventoryHas(doc, group.name)) return <p className="text-[13px] text-muted-foreground">{m.unavailable}</p>;
  const src: GroupSource = { fields: toVarSchema(group.schema).fields, stored: group.vars ?? {} };
  const hosts = groupHosts(doc, group.name);
  const host = hosts.some((h) => h.name === picked) ? picked : (hosts[0]?.name ?? '');
  const hostView = view === 'host' && host !== '';
  const inh = hostView ? hostInheritance(doc, group.name, host, src) : groupInheritance(doc, group.name, src);

  return (
    <div className="flex flex-col gap-3">
      <p className="text-xs text-muted-foreground">{m.intro}</p>
      <div className="flex flex-wrap items-center gap-2">
        <span id={viewLabel} className="text-[13px] text-muted-foreground">
          {m.view}
        </span>
        <div role="group" aria-labelledby={viewLabel} className="flex items-center gap-1">
          <Button type="button" variant={hostView ? 'ghost' : 'primary'} aria-pressed={!hostView} onClick={() => setView('group')}>
            {m.viewGroup(group.name)}
          </Button>
          <Button type="button" variant={hostView ? 'primary' : 'ghost'} aria-pressed={hostView} disabled={hosts.length === 0} onClick={() => setView('host')}>
            {m.viewHost}
          </Button>
        </div>
        {hostView && (
          <div className="w-56">
            <Select aria-label={m.host} options={hosts.map((h) => ({ value: h.name, label: h.name, hint: h.via.join(', ') }))} value={host} onChange={setPicked} />
          </div>
        )}
        {hosts.length === 0 && <span className="text-xs text-muted-foreground">{m.noHosts}</span>}
      </div>
      {inh.rows.length === 0 ? <p className="text-[13px] text-muted-foreground">{m.empty}</p> : <InheritanceTable inh={inh} m={m} />}
      <Legend inh={inh} hostView={hostView} m={m} />
    </div>
  );
}

function levelName(l: Level, m: M): string {
  return l.kind === 'default' ? m.schemaDefault : l.name;
}

function levelKind(l: Level, m: M): string {
  if (l.kind === 'default') return m.kind.default(l.name);
  if (l.kind === 'all' || l.kind === 'host') return m.kind[l.kind];
  return m.kind[l.role ?? 'other'];
}

function InheritanceTable({ inh, m }: { inh: Inheritance; m: M }) {
  return (
    <Card>
      <div className="overflow-x-auto">
        <table aria-label={m.table} className="w-full border-collapse text-[13px]">
          <thead>
            <tr className="border-b border-border text-left text-xs text-muted-foreground">
              <th scope="col" className="px-4 py-2 font-medium">
                {m.variable}
              </th>
              {inh.levels.map((l) => (
                <th key={l.id} scope="col" className="px-3 py-2 font-medium">
                  <span className={l.kind === 'default' ? 'block text-foreground' : 'block font-mono text-foreground'}>{levelName(l, m)}</span>
                  <span className="block font-normal">{levelKind(l, m)}</span>
                </th>
              ))}
              <th scope="col" className="px-4 py-2 font-medium">
                {m.result}
              </th>
            </tr>
          </thead>
          <tbody>
            {inh.rows.map((r) => {
              const w = inh.levels[r.winner];
              return (
                <tr key={r.key} data-testid={`inheritance-${r.key}`} className="border-b border-border last:border-b-0">
                  <th scope="row" className="px-4 py-2 text-left align-top font-mono font-normal">
                    {r.key}
                  </th>
                  {r.cells.map((c, i) => (
                    <td key={inh.levels[i].id} data-state={c.state} className="px-3 py-2 align-top">
                      <CellValue cell={c} notSet={m.notSet} />
                    </td>
                  ))}
                  <td className="px-4 py-2 align-top" data-result="">
                    <span className={w.kind === 'default' ? '' : 'font-mono'}>{levelName(w, m)}</span>
                    <span className="text-muted-foreground"> · {levelKind(w, m)}</span>
                    {r.tie.length > 0 && <span className="block text-xs text-muted-foreground">{m.tie(r.tie.join(', '))}</span>}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

/** CellValue is one level's value: the winner in the accent pill, an
 * overridden one struck through, an unset one a muted dot. A masked secret
 * is the renderer's mask and stays one. */
function CellValue({ cell, notSet }: { cell: Cell; notSet: string }) {
  if (cell.state === 'unset') {
    return (
      <span className="text-muted-foreground">
        <span aria-hidden="true">·</span>
        <span className="sr-only">{notSet}</span>
      </span>
    );
  }
  const text = formatValue(cell.value);
  const full = typeof cell.value === 'string' ? cell.value : JSON.stringify(cell.value);
  if (cell.state === 'win') {
    return (
      <span title={full} className="font-mono">
        <Pill tone="accent">{text}</Pill>
      </span>
    );
  }
  return (
    <s title={full} className="font-mono text-muted-foreground">
      {text}
    </s>
  );
}

function Legend({ inh, hostView, m }: { inh: Inheritance; hostView: boolean; m: M }) {
  const notes = [
    inh.levels.some((l) => l.kind === 'default') && m.notes.defaults,
    hostView && m.notes.host,
    inh.rows.some((r) => r.tie.length > 0) && m.notes.order,
  ].filter((n): n is string => !!n);
  return (
    <div className="flex flex-col gap-2 text-xs text-muted-foreground">
      <ul aria-label={m.legend.label} className="flex flex-wrap items-center gap-x-4 gap-y-1">
        <li className="flex items-center gap-1.5">
          <Pill tone="accent">{m.legend.sample}</Pill> {m.legend.win}
        </li>
        <li className="flex items-center gap-1.5">
          <s className="font-mono">{m.legend.sample}</s> {m.legend.lost}
        </li>
        <li className="flex items-center gap-1.5">
          <span aria-hidden="true">·</span> {m.legend.unset}
        </li>
      </ul>
      {notes.map((n) => (
        <p key={n}>{n}</p>
      ))}
    </div>
  );
}
