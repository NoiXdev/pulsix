import { Label } from '../../ui/label.tsx';
import { Select, type SelectOption } from '../../ui/Select.tsx';
import { useRepoPlaybooks, useRepos } from '../../api/hooks/useRepos.ts';
import type { Playbook } from '../../api/types.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';

/** What the person chose as a template's playbook. */
export interface PlaybookChoice {
  /** 'inline', a repository's id, or '' while it follows `playbookId`. */
  from: string;
  /** The registered playbook; '' for a repository path not registered yet
   * (the template's save registers it first, usePickRepoPlaybook). */
  playbookId: string;
  /** The path inside the repository; '' for an inline playbook. */
  path: string;
}

export interface PlaybookPickerProps {
  org: string;
  /** The organization's registered playbooks (inline and repository ones). */
  playbooks: readonly Playbook[];
  value: PlaybookChoice;
  onChange: (next: PlaybookChoice) => void;
  disabled?: boolean;
}

const INLINE = 'inline';

/** sourceOf is where a choice's playbook lives: the choice's own `from`,
 * else the registered playbook's (an existing template), else inline. */
function sourceOf(value: PlaybookChoice, playbooks: readonly Playbook[]): string {
  if (value.from) return value.from;
  const p = playbooks.find((x) => x.id === value.playbookId);
  return p?.source === 'repo' && p.repo_id ? p.repo_id : INLINE;
}

/**
 * PlaybookPicker chooses a template's playbook in two steps (backlog 7.75):
 * where it lives — the inline playbooks or one repository — and then the
 * playbook, which for a repository is one of the paths its last sync
 * discovered. A path nobody picked before has no playbook id yet; the
 * template dialog registers it on save. A discovered path the sync could not
 * read, or left out by a limit, says so instead of passing for an ordinary
 * playbook.
 */
export function PlaybookPicker({ org, playbooks, value, onChange, disabled }: PlaybookPickerProps) {
  const t = useT();
  const p = t.templates.playbookPicker;
  const fmt = useFormat();
  const repos = useRepos(org);
  const from = sourceOf(value, playbooks);
  const repoId = from === INLINE ? '' : from;
  const discovered = useRepoPlaybooks(org, repoId);
  const current = playbooks.find((x) => x.id === value.playbookId);
  const path = value.path || (current?.source === 'repo' ? (current.path ?? '') : '');

  const sources: SelectOption[] = [
    { value: INLINE, label: p.inline },
    ...(repos.data?.repos ?? []).map((r) => ({ value: r.id, label: r.name, hint: p.repository })),
  ];

  let options: SelectOption[];
  if (!repoId) {
    options = playbooks
      .filter((x) => x.source === 'editor')
      .map((x) => ({ value: x.id, label: x.summary || t.templates.playbook.editor }));
  } else {
    const rows = discovered.data?.playbooks ?? [];
    options = rows.map((d) => ({
      value: d.path,
      label: d.path,
      hint: d.failed ? p.notRead : d.left_out ? p.leftOut : p.fields(d.fields),
    }));
    // A playbook registered for this repository that the last sync did not
    // list (registered before discovery existed, or gone from the repo)
    // stays choosable, marked as such.
    for (const x of playbooks) {
      if (x.source === 'repo' && x.repo_id === repoId && x.path && !rows.some((d) => d.path === x.path)) {
        options.push({ value: x.path, label: x.path, hint: p.notDiscovered });
      }
    }
  }

  function pick(v: string) {
    if (!repoId) return onChange({ from: INLINE, playbookId: v, path: '' });
    const row = discovered.data?.playbooks?.find((d) => d.path === v);
    const known = playbooks.find((x) => x.source === 'repo' && x.repo_id === repoId && x.path === v);
    onChange({ from: repoId, playbookId: row?.playbook_id ?? known?.id ?? '', path: v });
  }

  const record = discovered.data?.sources;
  return (
    <div className="grid grid-cols-2 gap-3">
      <div className="flex flex-col gap-1">
        <Label htmlFor="template-playbook-source">{p.source}</Label>
        <Select
          id="template-playbook-source"
          disabled={disabled}
          options={sources}
          value={from}
          onChange={(v) => onChange({ from: v, playbookId: '', path: '' })}
        />
      </div>
      <div className="flex flex-col gap-1">
        <Label htmlFor="template-playbook">{t.templates.dialog.playbook}</Label>
        <Select
          id="template-playbook"
          required
          disabled={disabled}
          options={options}
          value={repoId ? path : value.playbookId}
          onChange={pick}
          placeholder={t.templates.dialog.choosePlaybook}
        />
        {repoId && discovered.data && !record && <span className="text-[11px] text-muted-foreground">{p.notSynced}</span>}
        {repoId && record && (
          <span className="text-[11px] text-muted-foreground">
            <span>{p.commit(record.commit.slice(0, 7))}</span> · {p.syncedAt(fmt.dateTime(record.synced_at))}
          </span>
        )}
      </div>
    </div>
  );
}
