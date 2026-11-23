import { useMemo, useState, type CSSProperties } from 'react';
import { useT } from '../../i18n/useT.ts';
import { lineDiff, MAX_COMPARE_LINES, type DiffHunk } from './lineDiff.ts';

/** A longer diff shows this many rows until "Show all" is pressed. */
export const MAX_SHOWN_LINES = 400;

/** A prepared diff past this many lines is cut there, even for "Show all":
 * what a task reports is not bounded, and the page must stay usable. */
export const MAX_PREPARED_LINES = 20_000;

type RowKind = 'header' | 'hunk' | 'context' | 'delete' | 'insert' | 'note';

interface Row {
  kind: RowKind;
  text: string;
  /** The note that a prepared diff was cut: no line of the diff. */
  cut?: true;
}

export type UnifiedDiffProps =
  | { before: string; after: string; beforeHeader?: string; afterHeader?: string; prepared?: never }
  | { prepared: string; before?: never; after?: never; beforeHeader?: never; afterHeader?: never };

const ROW_STYLE: Record<RowKind, CSSProperties> = {
  header: { color: 'var(--muted-foreground)', fontWeight: 600 },
  hunk: { color: 'var(--px-accent)' },
  context: {},
  delete: { color: 'var(--px-crit)', background: 'color-mix(in srgb, var(--px-crit) 10%, transparent)' },
  insert: { color: 'var(--px-ok)', background: 'color-mix(in srgb, var(--px-ok) 10%, transparent)' },
  note: { color: 'var(--muted-foreground)' },
};

const PREFIX: Record<'context' | 'delete' | 'insert', string> = { context: ' ', delete: '-', insert: '+' };

function hunkRows(hunks: readonly DiffHunk[], noNewline: string): Row[] {
  return hunks.flatMap((h) => [
    { kind: 'hunk' as const, text: `@@ -${h.oldStart},${h.oldLines} +${h.newStart},${h.newLines} @@` },
    ...h.lines.flatMap((l): Row[] => [{ kind: l.kind, text: `${PREFIX[l.kind]}${l.text}` }, ...(l.noNewline ? [{ kind: 'note' as const, text: noNewline }] : [])]),
  ]);
}

/** A ready unified diff, line by line, its kind read from its first characters. */
function preparedRows(text: string, cut: (shown: number, total: number) => string): Row[] {
  const body = text.replace(/\n$/, '');
  // Count the lines first: splitting a huge text only to throw most away is the cost to avoid.
  let total = 1;
  for (let at = body.indexOf('\n'); at >= 0; at = body.indexOf('\n', at + 1)) total++;
  let lines: string[];
  if (total > MAX_PREPARED_LINES) {
    let end = -1;
    for (let n = 0; n < MAX_PREPARED_LINES; n++) end = body.indexOf('\n', end + 1);
    lines = body.slice(0, end).split('\n');
  } else {
    lines = body.split('\n');
  }
  const rows = lines.map((line): Row => {
    if (line.startsWith('--- ') || line.startsWith('+++ ')) return { kind: 'header', text: line };
    if (line.startsWith('@@')) return { kind: 'hunk', text: line };
    if (line.startsWith('-')) return { kind: 'delete', text: line };
    if (line.startsWith('+')) return { kind: 'insert', text: line };
    if (line.startsWith('\\')) return { kind: 'note', text: line };
    return { kind: 'context', text: line };
  });
  return total > MAX_PREPARED_LINES ? [...rows, { kind: 'note', text: cut(MAX_PREPARED_LINES, total), cut: true }] : rows;
}

/**
 * UnifiedDiff shows what a change does to a text, the way `diff -u` prints
 * it: removed lines red, added lines green, a few lines of context. It
 * compares `before` and `after` itself (lineDiff), or shows a `prepared`
 * unified diff as it is. A diff longer than MAX_SHOWN_LINES rows shows its
 * start and a "Show all" button; a side too large to compare is not
 * compared, a note says so. Presentational: it fetches nothing.
 */
export function UnifiedDiff(props: UnifiedDiffProps) {
  const t = useT();
  const d = t.ui.diff;
  const [all, setAll] = useState(false);
  const { prepared, before, after, beforeHeader, afterHeader } = props;
  const result = useMemo((): Row[] | 'tooLarge' => {
    if (prepared !== undefined) return preparedRows(prepared, d.cut);
    const diff = lineDiff(before, after);
    if (diff.kind === 'tooLarge') return 'tooLarge';
    if (diff.hunks.length === 0) return [];
    const headers: Row[] = [];
    if (beforeHeader !== undefined || afterHeader !== undefined) {
      headers.push({ kind: 'header', text: `--- ${beforeHeader ?? ''}` }, { kind: 'header', text: `+++ ${afterHeader ?? ''}` });
    }
    return [...headers, ...hunkRows(diff.hunks, d.noNewline)];
  }, [prepared, before, after, beforeHeader, afterHeader, d.noNewline, d.cut]);

  if (result === 'tooLarge') return <p className="text-xs text-muted-foreground">{d.tooLarge(MAX_COMPARE_LINES)}</p>;
  if (result.length === 0) return <p className="text-xs text-muted-foreground">{d.none}</p>;
  const shown = all ? result : result.slice(0, MAX_SHOWN_LINES);

  return (
    <figure aria-label={d.label} className="flex min-w-0 flex-col gap-1">
      <ol className="overflow-x-auto rounded-md border border-border bg-background py-1.5 font-mono text-[11px] leading-5">
        {shown.map((row, i) => (
          <li key={i} data-kind={row.kind} className="px-3 whitespace-pre" style={ROW_STYLE[row.kind]}>
            {row.text}
          </li>
        ))}
      </ol>
      {shown.length < result.length && (
        <button type="button" className="self-start text-[11px] font-medium text-primary hover:underline" onClick={() => setAll(true)}>
          {d.showAll(result.at(-1)?.cut ? result.length - 1 : result.length)}
        </button>
      )}
    </figure>
  );
}
