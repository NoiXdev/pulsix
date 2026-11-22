/**
 * lineDiff compares two texts line by line and answers unified-diff hunks
 * (what `diff -u` prints): the smallest edit (Myers' algorithm) with a few
 * lines of context around each change. Pure, so the dry-run result and the
 * run log render the same diffs from the same helper.
 */

/** Either side longer than this many lines is not compared at all. */
export const MAX_COMPARE_LINES = 5000;

/** Beyond this many edits the search stops and the rest of the differing
 * middle is shown as removed, then added: still a correct diff, only not
 * the smallest, and the search's memory stays bounded. */
const MAX_EDITS = 2000;

const DEFAULT_CONTEXT = 3;

export type DiffLineKind = 'context' | 'delete' | 'insert';

export interface DiffLine {
  kind: DiffLineKind;
  /** The line without its line break. */
  text: string;
  /** The line is the last of its text and has no line break. */
  noNewline?: boolean;
  /** 1-based line number in the old text (context and delete lines). */
  oldLine?: number;
  /** 1-based line number in the new text (context and insert lines). */
  newLine?: number;
}

export interface DiffHunk {
  oldStart: number;
  oldLines: number;
  newStart: number;
  newLines: number;
  lines: DiffLine[];
}

export type LineDiffResult = { kind: 'hunks'; hunks: DiffHunk[] } | { kind: 'tooLarge' };

/** Splits a text into its lines, each keeping its line break, so a last
 * line with and without one differ. */
function linesOf(text: string): string[] {
  if (text === '') return [];
  const parts = text.split('\n');
  const out = parts.slice(0, -1).map((p) => `${p}\n`);
  const last = parts[parts.length - 1];
  if (last !== '') out.push(last);
  return out;
}

type Op = 'equal' | 'delete' | 'insert';

/** Myers' O((N+M)·D) diff of a and b, as one op per line. Null when the
 * edit is longer than maxEdits. */
function myers(a: readonly string[], b: readonly string[], maxEdits: number): Op[] | null {
  const n = a.length;
  const m = b.length;
  const max = Math.min(n + m, maxEdits);
  const offset = max + 1;
  const v = new Int32Array(2 * max + 3);
  // trace[d] is v as it stood before step d, over k in [-d-1, d+1].
  const trace: Int32Array[] = [];
  for (let d = 0; d <= max; d++) {
    trace.push(v.slice(offset - d - 1, offset + d + 2));
    for (let k = -d; k <= d; k += 2) {
      let x = k === -d || (k !== d && v[offset + k - 1] < v[offset + k + 1]) ? v[offset + k + 1] : v[offset + k - 1] + 1;
      let y = x - k;
      while (x < n && y < m && a[x] === b[y]) {
        x++;
        y++;
      }
      v[offset + k] = x;
      if (x >= n && y >= m) return backtrack(trace, n, m);
    }
  }
  return null;
}

function backtrack(trace: readonly Int32Array[], n: number, m: number): Op[] {
  const ops: Op[] = [];
  let x = n;
  let y = m;
  for (let d = trace.length - 1; d >= 0; d--) {
    const v = trace[d];
    const at = (k: number) => v[k + d + 1];
    const k = x - y;
    const prevK = k === -d || (k !== d && at(k - 1) < at(k + 1)) ? k + 1 : k - 1;
    const prevX = at(prevK);
    const prevY = prevX - prevK;
    while (x > prevX && y > prevY) {
      ops.push('equal');
      x--;
      y--;
    }
    if (d > 0) ops.push(x === prevX ? 'insert' : 'delete');
    x = prevX;
    y = prevY;
  }
  return ops.reverse();
}

/** The ops of the differing middle (after the common prefix and suffix are
 * taken off), with the edit capped. */
function middleOps(a: readonly string[], b: readonly string[]): Op[] {
  if (a.length === 0) return b.map(() => 'insert');
  if (b.length === 0) return a.map(() => 'delete');
  return myers(a, b, MAX_EDITS) ?? [...a.map((): Op => 'delete'), ...b.map((): Op => 'insert')];
}

/** Within each run of changes, removed lines come before added ones. */
function deletesFirst(ops: Op[]): Op[] {
  const out: Op[] = [];
  let i = 0;
  while (i < ops.length) {
    if (ops[i] === 'equal') {
      out.push('equal');
      i++;
      continue;
    }
    let j = i;
    while (j < ops.length && ops[j] !== 'equal') j++;
    const run = ops.slice(i, j);
    out.push(...run.filter((o) => o === 'delete'), ...run.filter((o) => o === 'insert'));
    i = j;
  }
  return out;
}

function toLine(kind: DiffLineKind, raw: string, oldLine: number | undefined, newLine: number | undefined): DiffLine {
  const terminated = raw.endsWith('\n');
  const line: DiffLine = { kind, text: terminated ? raw.slice(0, -1) : raw };
  if (!terminated) line.noNewline = true;
  if (oldLine !== undefined) line.oldLine = oldLine;
  if (newLine !== undefined) line.newLine = newLine;
  return line;
}

/** One line per op, numbered, plus how many old and new lines precede each. */
function annotate(a: readonly string[], b: readonly string[], ops: readonly Op[]) {
  const lines: DiffLine[] = [];
  const before: { old: number; new: number }[] = [];
  let x = 0;
  let y = 0;
  for (const op of ops) {
    before.push({ old: x, new: y });
    if (op === 'equal') {
      lines.push(toLine('context', a[x], x + 1, y + 1));
      x++;
      y++;
    } else if (op === 'delete') {
      lines.push(toLine('delete', a[x], x + 1, undefined));
      x++;
    } else {
      lines.push(toLine('insert', b[y], undefined, y + 1));
      y++;
    }
  }
  return { lines, before };
}

function hunksOf(lines: readonly DiffLine[], before: readonly { old: number; new: number }[], context: number): DiffHunk[] {
  const hunks: DiffHunk[] = [];
  let i = 0;
  while (i < lines.length) {
    if (lines[i].kind === 'context') {
      i++;
      continue;
    }
    const start = Math.max(0, i - context);
    let end = i;
    let j = i + 1;
    while (j < lines.length) {
      if (lines[j].kind !== 'context') {
        end = j++;
        continue;
      }
      let k = j;
      while (k < lines.length && lines[k].kind === 'context') k++;
      if (k < lines.length && k - j <= 2 * context) {
        j = k;
        continue;
      }
      break;
    }
    const stop = Math.min(lines.length, end + 1 + context);
    const slice = lines.slice(start, stop);
    const oldLines = slice.filter((l) => l.kind !== 'insert').length;
    const newLines = slice.filter((l) => l.kind !== 'delete').length;
    hunks.push({
      oldStart: oldLines > 0 ? before[start].old + 1 : before[start].old,
      oldLines,
      newStart: newLines > 0 ? before[start].new + 1 : before[start].new,
      newLines,
      lines: slice,
    });
    i = stop;
  }
  return hunks;
}

export function lineDiff(beforeText: string, afterText: string, opts: { context?: number } = {}): LineDiffResult {
  const a = linesOf(beforeText);
  const b = linesOf(afterText);
  if (a.length > MAX_COMPARE_LINES || b.length > MAX_COMPARE_LINES) return { kind: 'tooLarge' };
  let prefix = 0;
  while (prefix < a.length && prefix < b.length && a[prefix] === b[prefix]) prefix++;
  let suffix = 0;
  while (suffix < a.length - prefix && suffix < b.length - prefix && a[a.length - 1 - suffix] === b[b.length - 1 - suffix]) suffix++;
  const middle = middleOps(a.slice(prefix, a.length - suffix), b.slice(prefix, b.length - suffix));
  const ops = deletesFirst([...Array.from({ length: prefix }, (): Op => 'equal'), ...middle, ...Array.from({ length: suffix }, (): Op => 'equal')]);
  const { lines, before } = annotate(a, b, ops);
  return { kind: 'hunks', hunks: hunksOf(lines, before, opts.context ?? DEFAULT_CONTEXT) };
}
