import { useState, type ReactNode } from 'react';
import { ChevronDown, ChevronRight, Copy } from 'lucide-react';
import { cn } from '../lib/utils.ts';
import { useT } from '../i18n/useT.ts';
import { Button } from './button.tsx';
import { useCopy } from './useCopy.ts';

export interface JsonViewerProps {
  /** The document to show; any JSON value. */
  value: unknown;
  /** Names the viewer's region. */
  label: string;
  /** Says a value is a masked secret, shown in the warning colour. `path`
   * is the value's dotted path (see `initiallyCollapsed`). */
  isMasked?: (value: unknown, path: string) => boolean;
  /** Paths of objects and arrays that start collapsed: keys joined with
   * dots, array indexes in brackets (`all.children`, `_meta.hostvars`,
   * `list[0]`); the document itself is the empty path. A dotted path is
   * ambiguous — an empty key spells the same path as its parent, a key
   * with a dot the same as a nested one (`{"a.b": …}` and `{"a": {"b": …}}`)
   * — and names every container it spells. Inside, lines and folds are
   * told apart by their key segments, never by this form. */
  initiallyCollapsed?: readonly string[];
}

type Token = 'key' | 'string' | 'number' | 'boolean' | 'null' | 'masked' | 'punct';

/** Syntax colours. The masked colour is the warning one; the rest are
 * calm hues kept apart from the state colours, which mean something else. */
const TOKEN_CLASS: Record<Token, string> = {
  key: 'text-brand',
  string: 'text-[var(--px-syntax-string)]',
  number: 'text-[var(--px-syntax-number)]',
  boolean: 'text-[var(--px-maintenance)]',
  null: 'text-muted-foreground italic',
  masked: 'text-warn',
  punct: 'text-muted-foreground',
};

/** A value's place in the document: its keys and indexes from the top. */
type Segments = readonly (string | number)[];

interface Line {
  /** Stable across folds, so a fold button keeps its element (and focus). */
  id: string;
  depth: number;
  content: ReactNode;
  /** The fold button of an object or array that opens on this line. */
  toggle?: { segments: Segments; expanded: boolean };
}

const isContainer = (v: unknown): v is Record<string, unknown> | unknown[] => typeof v === 'object' && v !== null;

const entriesOf = (v: Record<string, unknown> | unknown[]): [string | number, unknown][] =>
  Array.isArray(v) ? v.map((item, i) => [i, item]) : Object.entries(v);

/** The unambiguous key of a place, for line ids and the fold state. */
const keyOf = (segments: Segments) => JSON.stringify(segments);

/** The dotted path of the props (ambiguous, see `initiallyCollapsed`). */
function dottedPath(segments: Segments): string {
  return segments.reduce<string>((path, k) => (typeof k === 'number' ? `${path}[${k}]` : path === '' ? k : `${path}.${k}`), '');
}

/** A place as a fold button names it: the dotted path, with a key that
 * form cannot spell (empty, or with a dot, bracket or quote) in brackets. */
function shownPath(segments: Segments): string {
  return segments.reduce<string>((path, k) => {
    if (typeof k === 'number') return `${path}[${k}]`;
    if (k === '' || /[.[\]"]/.test(k)) return `${path}[${JSON.stringify(k)}]`;
    return path === '' ? k : `${path}.${k}`;
  }, '');
}

/** Every non-empty object and array in `v`, by place. */
function containers(v: unknown, segments: Segments = [], out: Segments[] = []): Segments[] {
  if (!isContainer(v)) return out;
  const entries = entriesOf(v);
  if (entries.length === 0) return out;
  out.push(segments);
  for (const [k, child] of entries) containers(child, [...segments, k], out);
  return out;
}

/**
 * JsonViewer shows a JSON document the way an editor would: syntax
 * colours, line numbers, objects and arrays that fold (a folded one says
 * how many keys or items it holds), and a toolbar with Expand all,
 * Collapse all (everything below the top level) and Copy. Copy always
 * takes the whole document pretty-printed, folded or not, with every value
 * as it is shown — a masked secret stays masked. Presentational: it shows
 * what it is given.
 */
export function JsonViewer({ value, label, isMasked, initiallyCollapsed }: JsonViewerProps) {
  const t = useT();
  const j = t.ui.jsonViewer;
  const copy = useCopy();
  // The folded containers, by keyOf.
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(() => {
    const wanted = new Set(initiallyCollapsed);
    return new Set(containers(value).filter((c) => wanted.has(dottedPath(c))).map(keyOf));
  });

  const toggle = (segments: Segments) =>
    setCollapsed((cur) => {
      const next = new Set(cur);
      const key = keyOf(segments);
      if (!next.delete(key)) next.add(key);
      return next;
    });

  const lines: Line[] = [];

  function scalar(v: unknown, segments: Segments): ReactNode {
    const text = JSON.stringify(v) ?? 'null';
    if (isMasked?.(v, dottedPath(segments))) {
      return (
        <span data-token="masked" title={j.masked} className={TOKEN_CLASS.masked}>
          {text}
        </span>
      );
    }
    const kind: Token = v === null ? 'null' : typeof v === 'string' ? 'string' : typeof v === 'number' ? 'number' : typeof v === 'boolean' ? 'boolean' : 'null';
    return (
      <span data-token={kind} className={TOKEN_CLASS[kind]}>
        {text}
      </span>
    );
  }

  function walk(v: unknown, segments: Segments, depth: number, key: string | undefined, comma: boolean) {
    const id = keyOf(segments);
    const keyPart = key !== undefined && (
      <>
        <span data-token="key" className={TOKEN_CLASS.key}>
          {JSON.stringify(key)}
        </span>
        <span className={TOKEN_CLASS.punct}>: </span>
      </>
    );
    const tail = comma ? <span className={TOKEN_CLASS.punct}>,</span> : null;
    if (!isContainer(v)) {
      lines.push({ id, depth, content: <>{keyPart}{scalar(v, segments)}{tail}</> });
      return;
    }
    const entries = entriesOf(v);
    const [open, close] = Array.isArray(v) ? ['[', ']'] : ['{', '}'];
    if (entries.length === 0) {
      lines.push({ id, depth, content: <>{keyPart}<span className={TOKEN_CLASS.punct}>{open + close}</span>{tail}</> });
      return;
    }
    if (collapsed.has(id)) {
      const count = Array.isArray(v) ? j.items(entries.length) : j.keys(entries.length);
      lines.push({
        id: `${id}:open`,
        depth,
        toggle: { segments, expanded: false },
        content: (
          <>
            {keyPart}
            <span className={TOKEN_CLASS.punct}>{`${open} … ${close}`}</span>
            {tail}
            <span className="ml-2 font-sans text-[11px] text-muted-foreground">{count}</span>
          </>
        ),
      });
      return;
    }
    lines.push({ id: `${id}:open`, depth, toggle: { segments, expanded: true }, content: <>{keyPart}<span className={TOKEN_CLASS.punct}>{open}</span></> });
    entries.forEach(([k, child], i) => walk(child, [...segments, k], depth + 1, typeof k === 'string' ? k : undefined, i < entries.length - 1));
    lines.push({ id: `${id}:close`, depth, content: <><span className={TOKEN_CLASS.punct}>{close}</span>{tail}</> });
  }
  walk(value, [], 0, undefined, false);

  return (
    <section aria-label={label} className="flex min-w-0 flex-col overflow-hidden rounded-[10px] border border-border bg-background">
      {/* Three plain buttons, each a tab stop: no toolbar role, which would
          promise arrow-key roving. */}
      <div className="flex flex-wrap items-center justify-end gap-1 border-b border-border px-2 py-1.5">
        <Button variant="ghost" className="h-7 px-2 text-[12px]" onClick={() => setCollapsed(new Set())}>
          {j.expandAll}
        </Button>
        <Button variant="ghost" className="h-7 px-2 text-[12px]" onClick={() => setCollapsed(new Set(containers(value).filter((c) => c.length > 0).map(keyOf)))}>
          {j.collapseAll}
        </Button>
        <Button variant="ghost" className="h-7 px-2 text-[12px]" onClick={() => void copy(JSON.stringify(value, null, 2) ?? 'null', j.copied)}>
          <Copy size={13} /> {j.copy}
        </Button>
      </div>
      <div className="max-h-[560px] overflow-auto py-2 font-mono text-[12px] leading-relaxed">
        {lines.map((line, i) => (
          <div key={line.id} className="flex min-w-max">
            <span data-line-number aria-hidden="true" className="w-10 shrink-0 pr-3 text-right text-muted-foreground/60 select-none">
              {i + 1}
            </span>
            <span className="flex w-4 shrink-0 items-center justify-center">
              {line.toggle && (
                <FoldButton
                  label={j.toggle(line.toggle.segments.length === 0 ? j.root : shownPath(line.toggle.segments))}
                  expanded={line.toggle.expanded}
                  onClick={() => toggle(line.toggle!.segments)}
                />
              )}
            </span>
            <code className="pr-4 whitespace-pre" style={{ paddingLeft: `${line.depth * 2}ch` }}>
              {line.content}
            </code>
          </div>
        ))}
      </div>
    </section>
  );
}

function FoldButton({ label, expanded, onClick }: { label: string; expanded: boolean; onClick: () => void }) {
  const Icon = expanded ? ChevronDown : ChevronRight;
  return (
    <button
      type="button"
      aria-label={label}
      aria-expanded={expanded}
      onClick={onClick}
      className={cn('rounded text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary')}
    >
      <Icon size={12} />
    </button>
  );
}
