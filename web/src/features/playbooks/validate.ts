/**
 * Client-side mirror of internal/playbooks/validate.go (ValidateYAML), for
 * the playbook editor's before-submit preview. The messages are part of the
 * surface — they are the server's own, shown as-is. The server remains the
 * authority: a document using a construct this minimal parser does not
 * model (anchors, aliases, block scalars) passes the preview and is judged
 * by the server's 422 instead.
 *
 * Like the server, only the first YAML document is considered (the same
 * rule Ansible's own loader applies) and only the first play is inspected.
 */

export const MAX_INLINE_BYTES = 512 * 1024;

const KEY_RE = /^("(?:[^"\\]|\\.)*"|'(?:[^']|'')*'|[^:#]*?)\s*:(?:\s+(.*))?\s*$/;

const PLAIN_NULL = new Set(['~', 'null', 'Null', 'NULL']);
const PLAIN_BOOL = new Set(['true', 'True', 'TRUE', 'false', 'False', 'FALSE']);

/** validateYAML returns the server's message for the first problem it
 * finds, or null when the document passes (or is outside the subset). */
export function validateYAML(inline: string): string | null {
  if (new TextEncoder().encode(inline).length > MAX_INLINE_BYTES) {
    return 'playbook exceeds 512 KiB';
  }
  let doc: string[] | null;
  try {
    doc = firstDocument(inline);
  } catch (e) {
    return `malformed YAML: ${e instanceof Error ? e.message : 'could not parse'}`;
  }
  if (doc === null) return null; // constructs outside the subset: the server judges
  if (doc.every((l) => l.trim() === '')) return 'expected a list of plays';

  let root: Node;
  try {
    root = parseBlock(doc);
  } catch (e) {
    return `malformed YAML: ${e instanceof Error ? e.message : 'could not parse'}`;
  }
  if (root.kind === 'skip') return null;
  if (root.kind !== 'seq' || root.items.length === 0) return 'expected a list of plays';

  const play = root.items[0];
  if (play.kind === 'skip') return null;
  if (play.kind !== 'map') return 'play 1: must be a mapping';
  const hosts = play.entries.find(([key]) => key === 'hosts');
  if (!hosts || hosts[1].kind === 'null' || (hosts[1].kind === 'scalar' && hosts[1].text === '')) return 'play 1: missing hosts';
  if (hosts[1].kind === 'nested' || hosts[1].kind !== 'scalar' || hosts[1].type !== 'string') return 'play 1: hosts must be a string';
  return null;
}

/**
 * Node is the tiny YAML model the three questions need: a mapping (with
 * its entries, in document order), a sequence (with its items), a scalar
 * (raw text plus the go-yaml scalar type), null, a nested block (a value
 * that continues on deeper lines — a sequence or mapping, never a string)
 * or skip (a construct outside the subset).
 */
type MapNode = { kind: 'map'; entries: [string, Node][] };
type SeqNode = { kind: 'seq'; items: Node[] };
type ScalarNode = { kind: 'scalar'; text: string; type: 'string' | 'int' | 'float' | 'bool' };
type NullNode = { kind: 'null' };
type NestedNode = { kind: 'nested' };
type SkipNode = { kind: 'skip' };
type Node = MapNode | SeqNode | ScalarNode | NullNode | NestedNode | SkipNode;

/**
 * firstDocument returns the first YAML document's lines, comments stripped,
 * or null when it uses a construct outside the subset. Tab indentation is
 * the one malformed shape detected up front (go-yaml: "found character
 * that cannot start any token").
 */
function firstDocument(inline: string): string[] | null {
  const lines: (string | null)[] = inline.split('\n').map(stripComment);
  for (const l of lines) {
    if (l === null) return null; // anchors, aliases, block scalars
    if (/^ *\t/.test(l) || /:\t/.test(l) || /^-\t/.test(l)) {
      throw new Error('found character that cannot start any token');
    }
  }
  const clean = lines as string[];

  // Documents are separated by a lone `---` or `...`; directives (%) are
  // skipped. The first document is the first segment with content — a
  // leading `---` makes that the second segment.
  const segments: string[][] = [[]];
  for (const line of clean) {
    const t = line.trim();
    if (t.startsWith('%')) continue;
    if (t === '---' || t === '...') {
      segments.push([]);
      continue;
    }
    segments[segments.length - 1].push(line);
  }
  return segments[0].length > 0 ? segments[0] : (segments[1] ?? []);
}

/** stripComment removes a trailing `# …` comment outside quotes and flags
 * the constructs the subset does not model (anchors &x, aliases *x, block
 * scalars | and >). */
function stripComment(raw: string): string | null {
  if (/(^|\s)[&*][\w]/.test(raw)) return null;
  if (/(^|\s)[-|][-+]?\s*$/.test(raw.replace(/[ \t]+$/, ''))) return null;
  if (/(^|\s)\|/.test(raw) || /(^|\s)>/.test(raw)) return null;
  let inSingle = false;
  let inDouble = false;
  for (let i = 0; i < raw.length; i++) {
    const c = raw[i];
    if (c === "'" && !inDouble) inSingle = !inSingle;
    else if (c === '"' && !inSingle) inDouble = !inDouble;
    else if (c === '#' && !inSingle && !inDouble && (i === 0 || /\s/.test(raw[i - 1] ?? ''))) {
      return raw.slice(0, i).trimEnd();
    }
  }
  return raw;
}

/** indentOf counts a line's leading spaces. */
function indentOf(line: string): number {
  const m = /^ */.exec(line);
  return m ? m[0].length : 0;
}

/** trimmed drops blank lines (go-yaml ignores them between nodes). */
function trimmed(lines: string[]): string[] {
  return lines.filter((l) => l.trim() !== '');
}

/**
 * parseBlock classifies one document: a sequence of plays, a mapping, a
 * flow node, or a plain scalar — exactly the root-shape question
 * ValidateYAML answers first.
 */
function parseBlock(linesIn: string[]): Node {
  const lines = trimmed(linesIn);
  const base = indentOf(lines[0]);
  const first = lines[0].trim();

  if (first.startsWith('- ') || first === '-') return parseSequence(lines, base);
  if (KEY_RE.test(first)) return { kind: 'map', entries: [] }; // a mapping root is not a list of plays
  if (first.startsWith('{') || first.startsWith('[')) return parseFlow(lines.join(' '));
  return scalarNode(first);
}

/**
 * parseSequence walks the document's list: items at `base`, an item's own
 * mapping starting inline after `- ` (or on deeper lines), deeper lines
 * belonging to the item, and a dedent below `base` malformed (it would
 * start a second root node).
 */
function parseSequence(lines: string[], base: number): Node {
  const items: Node[] = [];
  let i = 0;
  while (i < lines.length) {
    const ind = indentOf(lines[i]);
    if (ind !== base) throw new Error('did not find expected key');
    const t = lines[i].trim();
    if (t === '-') {
      const body: string[] = [];
      i++;
      while (i < lines.length && indentOf(lines[i]) > base) {
        body.push(lines[i]);
        i++;
      }
      if (body.length === 0) {
        items.push({ kind: 'null' });
        continue;
      }
      const keyIndent = indentOf(body[0]);
      items.push(parseItemBlock(body.map((l) => l.slice(keyIndent)), 0));
      continue;
    }
    const itemIndent = base + 2;
    const body = [lines[i].slice(itemIndent)];
    i++;
    while (i < lines.length && indentOf(lines[i]) > base) {
      if (indentOf(lines[i]) < itemIndent) throw new Error('did not find expected key');
      body.push(lines[i].slice(itemIndent));
      i++;
    }
    items.push(parseItemBlock(body, 0));
  }
  return { kind: 'seq', items };
}

/** parseItemBlock parses one sequence entry from its own lines
 * (continuation lines already sliced to the entry's indent). */
function parseItemBlock(body: string[], keyIndent: number): Node {
  const lines = trimmed(body);
  const first = lines[0].trim();
  if (first.startsWith('{')) return parseFlow(lines.join(' '));
  if (KEY_RE.test(first)) return parseMapping(lines, keyIndent);
  return scalarNode(first);
}

/**
 * parseMapping reads one block mapping: entries at exactly keyIndent,
 * deeper lines part of the previous value's block, a dedent below
 * keyIndent malformed here.
 */
function parseMapping(lines: string[], keyIndent: number): Node {
  const entries: [string, Node][] = [];
  for (let i = 0; i < lines.length; i++) {
    const ind = indentOf(lines[i]);
    if (ind < keyIndent) throw new Error('did not find expected key');
    if (ind > keyIndent) continue; // the previous entry's nested block
    const m = KEY_RE.exec(lines[i].trim());
    if (!m) throw new Error(`could not parse line: ${lines[i].trim()}`);
    const key = unquote(m[1].trim());
    const raw = (m[2] ?? '').trim();
    // A key with nothing after the colon whose next line is deeper holds a
    // block value (a nested sequence or mapping, never a string scalar).
    const nested = raw === '' && i + 1 < lines.length && indentOf(lines[i + 1]) > keyIndent;
    entries.push([key, nested ? { kind: 'nested' } : rawValue(raw)]);
  }
  return { kind: 'map', entries };
}

/** rawValue classifies one inline value. */
function rawValue(raw: string): Node {
  if (raw === '') return { kind: 'null' };
  if (raw.startsWith('[') || raw.startsWith('{')) return parseFlow(raw);
  return scalarNode(raw);
}

/** scalarNode classifies a plain or quoted scalar, go-yaml style: quotes
 * make strings, ~/null is null, true/false bool, numerics int/float,
 * everything else a plain string. */
function scalarNode(raw: string): Node {
  if (raw === '') return { kind: 'null' };
  if (raw.startsWith('"')) {
    if (!/^"(?:[^"\\]|\\.)*"$/.test(raw)) throw new Error(`found unexpected end of stream: ${raw}`);
    return { kind: 'scalar', text: unescapeDouble(raw.slice(1, -1)), type: 'string' };
  }
  if (raw.startsWith("'")) {
    if (!/^'(?:[^']|'')*'$/.test(raw)) throw new Error(`found unexpected end of stream: ${raw}`);
    return { kind: 'scalar', text: raw.slice(1, -1).replace(/''/g, "'"), type: 'string' };
  }
  if (PLAIN_NULL.has(raw)) return { kind: 'null' };
  if (PLAIN_BOOL.has(raw)) return { kind: 'scalar', text: raw, type: 'bool' };
  if (/^[+-]?\d+$/.test(raw)) return { kind: 'scalar', text: raw, type: 'int' };
  if (/^[+-]?\d+\.\d*$/.test(raw)) return { kind: 'scalar', text: raw, type: 'float' };
  return { kind: 'scalar', text: raw, type: 'string' };
}

function unquote(s: string): string {
  if ((s.startsWith('"') && s.endsWith('"')) || (s.startsWith("'") && s.endsWith("'"))) return s.slice(1, -1);
  return s;
}

function unescapeDouble(s: string): string {
  return s.replace(/\\"/g, '"').replace(/\\n/g, '\n').replace(/\\\\/g, '\\');
}

/**
 * parseFlow parses one balanced flow node ({…} or […]) spanning the whole
 * text. Only the shallow nesting ValidateYAML can meet is modelled:
 * scalars, quoted scalars, and the brackets themselves.
 */
function parseFlow(srcRaw: string): Node {
  const src = srcRaw.trim();
  const isMap = src.startsWith('{');
  const open = isMap ? '{' : '[';
  const close = isMap ? '}' : ']';
  let depth = 0;
  let inSingle = false;
  let inDouble = false;
  for (let i = 0; i < src.length; i++) {
    const c = src[i];
    if (c === "'" && !inDouble) inSingle = !inSingle;
    else if (c === '"' && !inSingle) inDouble = !inDouble;
    else if (!inSingle && !inDouble) {
      if (c === open) depth++;
      else if (c === close) {
        depth--;
        if (depth === 0) {
          if (src.slice(i + 1).trim() !== '') throw new Error('did not find expected key');
          return isMap ? flowMapping(src.slice(1, i)) : flowSequence(src.slice(1, i));
        }
      }
    }
  }
  throw new Error('found unexpected end of stream');
}

/** splitFlow splits a flow node's inner text on its top-level commas. */
function splitFlow(inner: string): string[] {
  const parts: string[] = [];
  let depth = 0;
  let inSingle = false;
  let inDouble = false;
  let cur = '';
  for (const c of inner) {
    if (c === "'" && !inDouble) inSingle = !inSingle;
    else if (c === '"' && !inSingle) inDouble = !inDouble;
    if (!inSingle && !inDouble) {
      if (c === '{' || c === '[') depth++;
      else if (c === '}' || c === ']') depth--;
      else if (c === ',' && depth === 0) {
        parts.push(cur.trim());
        cur = '';
        continue;
      }
    }
    cur += c;
  }
  if (cur.trim() !== '' || parts.length > 0) parts.push(cur.trim());
  return parts;
}

function flowSequence(inner: string): Node {
  const parts = splitFlow(inner).filter((p) => p !== '');
  return { kind: 'seq', items: parts.map((p) => rawValue(p)) };
}

function flowMapping(inner: string): Node {
  const entries: [string, Node][] = [];
  for (const part of splitFlow(inner)) {
    if (part === '') continue;
    const m = KEY_RE.exec(part);
    if (!m) throw new Error(`could not parse flow mapping entry: ${part}`);
    entries.push([unquote(m[1].trim()), rawValue((m[2] ?? '').trim())]);
  }
  return { kind: 'map', entries };
}
