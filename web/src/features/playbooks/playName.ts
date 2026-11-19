/**
 * firstPlayName is the `name:` of the YAML's first play, as the new page
 * shows the playbook's name before it is created: an editor playbook has no
 * name of its own, the server names it by its first play (Playbook.Summary
 * in internal/playbooks/service.go). '' when the first play has none or the
 * text is not a list of plays. A line-based reading of plain block YAML.
 */
export function firstPlayName(yaml: string): string {
  let keyIndent = -1;
  for (const raw of yaml.split('\n')) {
    const trimmed = raw.trim();
    if (trimmed === '' || trimmed.startsWith('#') || trimmed === '---') continue;
    const indent = raw.length - raw.trimStart().length;
    const item = /^(\s*-\s+)(.*)$/.exec(raw);
    let rest: string;
    if (keyIndent < 0) {
      if (!item) return '';
      keyIndent = item[1].length;
      rest = item[2];
    } else {
      if (indent < keyIndent) return '';
      if (indent > keyIndent) continue;
      rest = trimmed;
    }
    const name = /^name:\s*(.*?)\s*$/.exec(rest);
    if (name) return unquote(name[1]);
  }
  return '';
}

function unquote(value: string): string {
  const m = /^"(.*)"$/.exec(value) ?? /^'(.*)'$/.exec(value);
  return m ? m[1] : value;
}
