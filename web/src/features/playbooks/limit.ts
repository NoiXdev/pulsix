/**
 * A template's limit as the dry run form can read it: which host names the
 * pattern keeps. It mirrors the manager's enqueue gate (internal/jobs/limit.go):
 * ':'- or ','-separated terms, each a literal name, a glob ('*', '?', a plain
 * '[...]' class) or its exclusion with a leading '!'; matching is
 * case-sensitive. Anything else (regex, '&', file lists, braces, …) is
 * undecidable here, and an undecidable limit keeps every host — Ansible
 * applies the real limit when the job runs.
 */

const NAME_CHAR = /^[A-Za-z0-9._-]$/;

/** globRegExp turns one term into a RegExp, or null outside the grammar. */
function globRegExp(pat: string): RegExp | null {
  let out = '';
  for (let i = 0; i < pat.length; ) {
    const c = pat[i];
    if (NAME_CHAR.test(c)) {
      out += c === '.' ? '\\.' : c;
      i++;
    } else if (c === '*') {
      out += '.*';
      i++;
    } else if (c === '?') {
      out += '.';
      i++;
    } else if (c === '[') {
      const end = pat.indexOf(']', i + 1);
      const body = end < 0 ? '' : pat.slice(i + 1, end);
      if (body === '' || ![...body].every((ch) => NAME_CHAR.test(ch))) return null;
      out += `[${body.replace(/\./g, '\\.')}]`;
      i = end + 1;
    } else {
      return null;
    }
  }
  try {
    return new RegExp(`^${out}$`);
  } catch {
    // A class the RegExp refuses (a reversed range such as [9-0]) is
    // undecidable here, never an error while the form renders.
    return null;
  }
}

/** limitMatcher answers whether a host name passes `limit`. */
export function limitMatcher(limit: string): (name: string) => boolean {
  const terms = limit.split(/[:,]/).filter((raw) => raw !== '');
  const parsed: { exclude: boolean; re: RegExp }[] = [];
  for (const raw of terms) {
    const exclude = raw.startsWith('!');
    const re = globRegExp(exclude ? raw.slice(1) : raw);
    if (!re) return () => true;
    parsed.push({ exclude, re });
  }
  const hasPositive = parsed.some((t) => !t.exclude);
  return (name) => {
    let positive = false;
    for (const t of parsed) {
      const m = t.re.test(name);
      if (t.exclude) {
        if (m) return false;
      } else if (m) {
        positive = true;
      }
    }
    return positive || !hasPositive;
  };
}
