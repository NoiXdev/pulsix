/**
 * The variables a playbook's YAML uses, as a hint for the playbook page's
 * side column: every Jinja expression (`{{ name }}`, `{{ name.attr | f }}`)
 * gives its top-level name, and each name says where its value comes from.
 *
 * - `vars`: the play it is used in defines it in its own `vars:` mapping.
 * - `task`: a task sets it (`register:`, or a key of `set_fact`); those
 *   outlive their play, so any play counts.
 * - `external`: nothing in the playbook sets it, so the template, the
 *   inventory or a group must give it a value.
 *
 * This is a conservative line-based reading (the bundle carries no full YAML
 * parser; validate.ts is only a minimal one): it knows block mappings
 * indented as YAML writes them and a one-line flow mapping (`vars: { a: 1 }`),
 * not anchors or `vars_files`. Statements (`{% %}`), Jinja comments and YAML
 * comments are left out, and so are Ansible's own names (facts, magic
 * variables, `item` and a `loop_var`), literals and calls (`lookup(…)`).
 */

export type VariableSource = 'vars' | 'task' | 'external';

export interface PlaybookVariable {
  name: string;
  source: VariableSource;
}

/** Names Ansible itself provides; `ansible_*` is matched by its prefix. */
const BUILTINS = new Set([
  'item', 'hostvars', 'groups', 'group_names', 'inventory_hostname', 'inventory_hostname_short', 'inventory_dir', 'inventory_file',
  'play_hosts', 'playbook_dir', 'role_path', 'role_name', 'omit', 'vars', 'environment', 'lookup', 'query', 'q', 'range', 'now', 'undef',
]);

/** Jinja's words and literals, which are never a variable. */
const KEYWORDS = new Set(['not', 'and', 'or', 'in', 'is', 'if', 'else', 'true', 'false', 'none', 'True', 'False', 'None']);

const IDENT = /^[A-Za-z_]\w*/;
const KEY_LINE = /^(\s*)(?:-\s+)?(["']?)([A-Za-z_][\w.-]*)\2\s*:(.*)$/;

interface Line {
  indent: number;
  text: string;
}

/** stripComment drops a YAML comment (`# …` at the start or after a space,
 * outside quotes). */
function stripComment(line: string): string {
  let quote: string | null = null;
  for (let i = 0; i < line.length; i++) {
    const c = line[i];
    if (quote) {
      if (c === quote) quote = null;
    } else if (c === '"' || c === "'") {
      quote = c;
    } else if (c === '#' && (i === 0 || /\s/.test(line[i - 1]))) {
      return line.slice(0, i);
    }
  }
  return line;
}

/** The top-level name of one expression's body, or null. */
function topName(expr: string): string | null {
  let rest = expr.trim();
  for (;;) {
    rest = rest.replace(/^[(\s]+/, '');
    const m = IDENT.exec(rest);
    if (!m) return null;
    const word = m[0];
    rest = rest.slice(word.length);
    if (KEYWORDS.has(word)) continue;
    if (/^\s*\(/.test(rest)) return null; // a call
    return word;
  }
}

/** Every top-level name the line's `{{ }}` expressions use, in order. */
function usedNames(text: string): string[] {
  const clean = text.replace(/\{#[\s\S]*?#\}/g, '').replace(/\{%[\s\S]*?%\}/g, '');
  const names: string[] = [];
  for (const m of clean.matchAll(/\{\{([\s\S]*?)\}\}/g)) {
    const name = topName(m[1]);
    if (name) names.push(name);
  }
  return names;
}

/** The keys of a one-line flow mapping (`{ a: 1, "b": 2 }`). */
function flowKeys(value: string): string[] {
  const m = /^\s*\{(.*)\}\s*$/.exec(value);
  if (!m) return [];
  const keys: string[] = [];
  let depth = 0;
  let quote: string | null = null;
  let part = '';
  const flush = () => {
    const k = /^\s*(["']?)([A-Za-z_][\w-]*)\1\s*:/.exec(part);
    if (k) keys.push(k[2]);
    part = '';
  };
  for (const c of m[1]) {
    if (quote) {
      if (c === quote) quote = null;
    } else if (c === '"' || c === "'") quote = c;
    else if (c === '{' || c === '[') depth++;
    else if (c === '}' || c === ']') depth--;
    else if (c === ',' && depth === 0) {
      flush();
      continue;
    }
    part += c;
  }
  flush();
  return keys;
}

/** The keys of the block mapping nested under lines[at] (deeper than `indent`). */
function childKeys(lines: Line[], at: number, indent: number): string[] {
  const keys: string[] = [];
  let child = -1;
  for (let i = at + 1; i < lines.length; i++) {
    const l = lines[i];
    if (l.text.trim() === '') continue;
    if (l.indent <= indent) break;
    if (child < 0) child = l.indent;
    if (l.indent !== child) continue;
    const m = KEY_LINE.exec(l.text);
    if (m && !/^\s*-/.test(l.text)) keys.push(m[3]);
  }
  return keys;
}

/** The key a line starts (after an optional `- `) and the column it is at. */
function keyAt(text: string): { key: string; column: number; value: string } | null {
  const m = KEY_LINE.exec(text);
  if (!m) return null;
  const dash = /^(\s*)(-\s+)?/.exec(text)!;
  return { key: m[3], column: dash[0].length, value: m[4] };
}

/**
 * playbookVariables lists the names the playbook's expressions use, each
 * once in the order of first use, with where its value comes from.
 */
export function playbookVariables(yaml: string): PlaybookVariable[] {
  const lines: Line[] = yaml.split('\n').map((raw) => {
    const text = stripComment(raw);
    return { indent: text.length - text.trimStart().length, text };
  });

  // The plays: items of the first top-level sequence; their keys sit just
  // after the dash.
  let seqIndent = -1;
  let keyIndent = -1;
  let play = -1;
  const playOf: number[] = [];
  const playVars: Set<string>[] = [];
  const taskSet = new Set<string>();
  const skip = new Set<string>();

  lines.forEach((l, i) => {
    const t = l.text.trim();
    if (seqIndent < 0 && t !== '' && t !== '---' && t.startsWith('- ')) {
      seqIndent = l.indent;
    }
    if (seqIndent >= 0 && l.indent === seqIndent && /^-(\s|$)/.test(t)) {
      play++;
      playVars[play] = new Set();
      keyIndent = /^(\s*-\s+)/.exec(l.text)?.[1].length ?? seqIndent + 2;
    }
    playOf[i] = play;

    const k = keyAt(l.text);
    if (!k) return;
    if (play >= 0 && k.column === keyIndent && k.key === 'vars') {
      const keys = k.value.trim() ? flowKeys(k.value) : childKeys(lines, i, keyIndent);
      for (const key of keys) playVars[play].add(key);
    }
    if (k.key === 'register') {
      const name = IDENT.exec(k.value.trim())?.[0];
      if (name) taskSet.add(name);
    }
    if (k.key === 'loop_var') {
      const name = IDENT.exec(k.value.trim().replace(/^["']/, ''))?.[0];
      if (name) skip.add(name);
    }
    if (k.key === 'set_fact' || k.key === 'ansible.builtin.set_fact') {
      const keys = k.value.trim() ? flowKeys(k.value) : childKeys(lines, i, l.indent);
      for (const key of keys) if (key !== 'cacheable') taskSet.add(key);
    }
  });

  // Each name in the order of its first use, with every play it is used in.
  const order: string[] = [];
  const plays = new Map<string, Set<number>>();
  lines.forEach((l, i) => {
    for (const name of usedNames(l.text)) {
      if (BUILTINS.has(name) || name.startsWith('ansible_') || skip.has(name)) continue;
      if (!plays.has(name)) {
        plays.set(name, new Set());
        order.push(name);
      }
      plays.get(name)!.add(playOf[i]);
    }
  });

  return order.map((name) => {
    if (taskSet.has(name)) return { name, source: 'task' };
    const usedIn = [...plays.get(name)!];
    const inVars = usedIn.every((p) => p >= 0 && playVars[p]?.has(name));
    return { name, source: inVars ? 'vars' : 'external' };
  });
}
