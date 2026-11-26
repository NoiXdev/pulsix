import { Card } from '../../ui/table.tsx';
import { Section } from '../../ui/detail-layout.tsx';
import type { RunPicture } from '../../api/hooks/useRunRequests.ts';
import { useT } from '../../i18n/useT.ts';
import { presetLabel } from '../triggers/presets.ts';
import { describeSettle } from '../triggers/settle.ts';

export type PictureTrigger = NonNullable<RunPicture['trigger']>;
type TriggerState = NonNullable<PictureTrigger['new']>;

const TABLE = 'w-full text-xs';
const TH = 'px-4 py-2 text-left text-[11px] font-semibold tracking-wide text-muted-foreground uppercase';
const TD = 'border-t border-border px-4 py-2 align-top';

/**
 * TriggerRequestSections is the picture of a trigger request (spec
 * 2026-11-25 C): what the approval does (create, change, or give a new
 * secret), the URL senders post to, and the trigger as it is now next to
 * how the approval leaves it, with every setting that changes marked. The
 * secret is never part of it: the approval shows a new one once, in its own
 * dialog. It names the template revision a template run trigger approves
 * (and that the template changed since the request), and says when the
 * trigger or what it starts is gone.
 */
export function TriggerRequestSections({
  trigger,
  templateRevision,
  requestedRevision,
}: {
  trigger: PictureTrigger;
  /** The template revision the approval approves (the picture's); 0 for a
   * repository sync trigger, which runs no template. */
  templateRevision: number;
  /** The template revision when the request was made. */
  requestedRevision?: number;
}) {
  const t = useT();
  const m = t.triggers;
  const r = m.request;
  const { action, current } = trigger;
  // A rotation leaves every setting as it is; only the secret changes.
  const next: TriggerState | undefined = trigger.new ?? (action === 'rotate' ? current : undefined);
  const columns: { key: string; label: string; state: TriggerState }[] = [];
  if (current) columns.push({ key: 'current', label: r.current, state: current });
  if (next) columns.push({ key: 'next', label: r.next, state: next });

  const mapping = (s: TriggerState) => {
    const lines = Object.entries(s.mapping?.extra_vars ?? {})
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([name, path]) => `${path} → ${name}`);
    if (s.mapping?.limit) lines.push(`${s.mapping.limit} → limit`);
    return lines.length > 0 ? lines.join('\n') : r.none;
  };
  const rows: { key: string; label: string; value: (s: TriggerState) => string; mono?: boolean }[] = [
    {
      key: 'target',
      label: r.target,
      value: (s) =>
        s.kind === 'repo_sync'
          ? r.syncs(s.repo_name || s.repo_id || '', s.repo_url ?? '', s.repo_branch ?? '')
          : r.runs(s.template_name || s.template_id || ''),
    },
    { key: 'name', label: r.name, value: (s) => s.name },
    { key: 'state', label: r.state, value: (s) => (s.enabled ? m.enabled : m.disabled) },
    { key: 'sender', label: r.sender, value: (s) => `${presetLabel(s.preset, m.preset)} · ${s.signature_header}` },
    { key: 'replay', label: r.replay, value: (s) => s.timestamp_header || r.none, mono: true },
    { key: 'sources', label: r.sources, value: (s) => ((s.allow_cidrs ?? []).length > 0 ? (s.allow_cidrs ?? []).join('\n') : m.anySource), mono: true },
    { key: 'mapping', label: r.mapping, value: mapping, mono: true },
    { key: 'onActive', label: r.ifActive, value: (s) => m.form.onActive[s.on_active === 'skip' ? 'skip' : 'queue'] },
    { key: 'rate', label: r.rate, value: (s) => r.ratePer(s.rate_limit.count, s.rate_limit.per) },
    { key: 'settle', label: r.settle, value: (s) => describeSettle(s.settle_seconds, s.max_wait_seconds, m.settle) },
  ];
  const mapped = columns.some((c) => mapping(c.state) !== r.none);

  return (
    <Section id="trigger" title={r.section}>
      <div className="flex flex-col gap-3">
        {trigger.gone && (
          <p role="alert" className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive">
            {r.gone}
          </p>
        )}
        <p className="text-xs text-muted-foreground">{r.intro[action]}</p>
        <p className="flex flex-wrap items-center gap-2 text-xs">
          <span className="font-semibold">{r.url}</span>
          <code className="font-mono break-all">{trigger.url}</code>
        </p>
        {templateRevision > 0 && <p className="text-xs text-muted-foreground">{r.templateRevision(templateRevision)}</p>}
        {templateRevision > 0 && requestedRevision !== undefined && requestedRevision !== templateRevision && (
          <p role="note" className="rounded-md border border-[var(--px-warn)] px-3 py-2 text-xs text-[var(--px-warn)]">
            {r.templateChanged(requestedRevision)}
          </p>
        )}
        {columns.length > 0 && (
          <Card>
            <table className={TABLE}>
              <thead>
                <tr>
                  <th className={TH}>{r.field}</th>
                  {columns.map((c) => (
                    <th key={c.key} className={TH}>
                      {c.label}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => {
                  const values = columns.map((c) => row.value(c.state));
                  const changes = values.length === 2 && values[0] !== values[1];
                  return (
                    <tr key={row.key}>
                      <th scope="row" className={`${TD} text-left font-normal text-muted-foreground`}>
                        {row.label}
                      </th>
                      {values.map((v, i) => (
                        <td key={columns[i].key} className={`${TD} whitespace-pre-line break-all ${row.mono ? 'font-mono' : ''}`}>
                          {v}
                          {changes && i === 1 && <span className="ml-2 font-sans text-[11px] whitespace-nowrap text-[var(--px-warn)]">{r.changedMark}</span>}
                        </td>
                      ))}
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </Card>
        )}
        {mapped && <p className="text-xs text-muted-foreground">{r.mappingNote}</p>}
      </div>
    </Section>
  );
}
