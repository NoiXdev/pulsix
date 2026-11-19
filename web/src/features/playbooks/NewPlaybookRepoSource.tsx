import { Link } from '@tanstack/react-router';
import { buttonClass } from '../../ui/buttonStyles.ts';
import { DisabledHint } from '../../ui/detail-layout.tsx';
import { Label } from '../../ui/label.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { Select, type SelectOption } from '../../ui/Select.tsx';
import { useRepoPlaybooks, useRepos } from '../../api/hooks/useRepos.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';

/** A repository and a path inside it; '' while not chosen. */
export interface RepoChoice {
  repoId: string;
  path: string;
}

/**
 * NewPlaybookRepoSource picks a playbook from a repository: the repository,
 * then one of the paths its last sync discovered (each with its variables,
 * or why it could not be read). It is the template form's repository picker
 * (templates/PlaybookPicker.tsx) on the new page; Create registers the path
 * (or finds it registered) and opens its page.
 */
export function NewPlaybookRepoSource({ org, value, onChange }: { org: string; value: RepoChoice; onChange: (next: RepoChoice) => void }) {
  const t = useT();
  const n = t.playbooks.newPage;
  const fmt = useFormat();
  const repos = useRepos(org);
  const discovered = useRepoPlaybooks(org, value.repoId);

  if (!repos.data) return <QueryLoading />;
  const list = repos.data.repos ?? [];
  if (list.length === 0) {
    return (
      <DisabledHint
        title={n.noRepos}
        body={n.sourceRepoHint}
        action={
          <Link to="/o/$org/repos" params={{ org }} className={buttonClass('default')}>
            {n.addRepo}
          </Link>
        }
      />
    );
  }

  const options: SelectOption[] = (discovered.data?.playbooks ?? []).map((d) => ({
    value: d.path,
    label: d.path,
    hint: d.failed ? n.notRead : d.left_out ? n.leftOut : n.fields(d.fields),
  }));
  const record = discovered.data?.sources;

  return (
    <div className="grid max-w-3xl grid-cols-1 gap-3 md:grid-cols-2">
      <div className="flex flex-col gap-1">
        <Label htmlFor="new-playbook-repo">{n.repository}</Label>
        <Select
          id="new-playbook-repo"
          options={list.map((r) => ({ value: r.id, label: r.name, hint: r.branch }))}
          value={value.repoId}
          onChange={(repoId) => onChange({ repoId, path: '' })}
          placeholder={n.chooseRepository}
        />
      </div>
      <div className="flex flex-col gap-1">
        <Label htmlFor="new-playbook-path">{n.path}</Label>
        <Select
          id="new-playbook-path"
          disabled={!value.repoId}
          options={options}
          value={value.path}
          onChange={(path) => onChange({ ...value, path })}
          placeholder={n.choosePath}
        />
        {value.repoId && discovered.data && !record && <span className="text-[11px] text-muted-foreground">{n.notSynced}</span>}
        {value.repoId && record && (
          <span className="text-[11px] text-muted-foreground">
            {t.templates.playbookPicker.commit(record.commit.slice(0, 7))} · {t.templates.playbookPicker.syncedAt(fmt.dateTime(record.synced_at))}
          </span>
        )}
      </div>
    </div>
  );
}
