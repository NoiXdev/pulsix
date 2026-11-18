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
   * is the value's path (see `initiallyCollapsed`). */
  isMasked?: (value: unknown, path: string) => boolean;
  /** Paths of objects and arrays that start collapsed: keys joined with
   * dots, array indexes in brackets (`all.children`, `_meta.hostvars`,
   * `list[0]`); the document itself is the empty path. */
  initiallyCollapsed?: readonly string[];
}

type Token = 'key' | 'string' | 'number' | 'boolean' | 'null' | 'masked' | 'punct';

/** Syntax colours. The masked colour is the warning one; the rest are
 * calm hues kept apart from the state colours, which mean something else. */
const TOKEN_CLASS: Record<Token, string> = {
  key: 'text-brand',
  string: 'text-[#A5D6A7]',
  number: 'text-[#F9A66C]',
  boolean: 'text-[var(--px-maintenance)]',
  null: 'text-muted-foreground italic',
  masked: 'text-warn',
  punct: 'text-muted-foreground',
};

interface Line {
  /** Stable across folds, so a fold button keeps its element (and focus). */
  id: string;
  depth: number;
  content: ReactNode;
  /** The fold button of an object or array that opens on this line. */
  toggle?: { path: string; expanded: boolean };
}

const isContainer = (v: unknown): v is Record<string, unknown> | unknown[] => typeof v === 'object' && v !== null;

const entriesOf = (v: Record<string, unknown> | unknown[]): [string | number, unknown][] =>
  Array.isArray(v) ? v.map((item, i) => [i, item]) : Object.entries(v);

function childPath(parent: string, key: string | number): string {
  if (typeof key === 'number') return `${parent}[${key}]`;
  return parent === '' ? key : `${parent}.${key}`;
}

/** Every non-empty object and array in `v`, by path. */
function containerPaths(v: unknown, path = '', out: string[] = []): string[] {
  if (!isContainer(v)) return out;
  const entries = entriesOf(v);
  if (entries.length === 0) return out;
  out.push(path);
  for (const [k, child] of entries) containerPaths(child, childPath(path, k), out);
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
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(() => new Set(initiallyCollapsed));

  const toggle = (path: string) =>
    setCollapsed((cur) => {
      const next = new Set(cur);
      if (!next.delete(path)) next.add(path);
      return next;
    });

  const lines: Line[] = [];

  function scalar(v: unknown, path: string): ReactNode {
    const text = JSON.stringify(v) ?? 'null';
    if (isMasked?.(v, path)) {
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

  function walk(v: unknown, path: string, depth: number, key: string | undefined, comma: boolean) {
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
      lines.push({ id: path, depth, content: <>{keyPart}{scalar(v, path)}{tail}</> });
      return;
    }
    const entries = entriesOf(v);
    const [open, close] = Array.isArray(v) ? ['[', ']'] : ['{', '}'];
    if (entries.length === 0) {
      lines.push({ id: path, depth, content: <>{keyPart}<span className={TOKEN_CLASS.punct}>{open + close}</span>{tail}</> });
      return;
    }
    if (collapsed.has(path)) {
      const count = Array.isArray(v) ? j.items(entries.length) : j.keys(entries.length);
      lines.push({
        id: `${path}:open`,
        depth,
        toggle: { path, expanded: false },
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
    lines.push({ id: `${path}:open`, depth, toggle: { path, expanded: true }, content: <>{keyPart}<span className={TOKEN_CLASS.punct}>{open}</span></> });
    entries.forEach(([k, child], i) => walk(child, childPath(path, k), depth + 1, typeof k === 'string' ? k : undefined, i < entries.length - 1));
    lines.push({ id: `${path}:close`, depth, content: <><span className={TOKEN_CLASS.punct}>{close}</span>{tail}</> });
  }
  walk(value, '', 0, undefined, false);

  return (
    <section aria-label={label} className="flex min-w-0 flex-col overflow-hidden rounded-[10px] border border-border bg-background">
      <div role="toolbar" aria-label={j.toolbar} className="flex flex-wrap items-center justify-end gap-1 border-b border-border px-2 py-1.5">
        <Button variant="ghost" className="h-7 px-2 text-[12px]" onClick={() => setCollapsed(new Set())}>
          {j.expandAll}
        </Button>
        <Button variant="ghost" className="h-7 px-2 text-[12px]" onClick={() => setCollapsed(new Set(containerPaths(value).filter((p) => p !== '')))}>
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
                  label={j.toggle(line.toggle.path === '' ? j.root : line.toggle.path)}
                  expanded={line.toggle.expanded}
                  onClick={() => toggle(line.toggle!.path)}
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
