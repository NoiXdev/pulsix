import { useId } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { FlaskConical, Save } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { Pill, Tag } from '../../ui/badge.tsx';
import { RowMenu, RowMenuItem } from '../../ui/list-row.tsx';
import { useConfirm } from '../../ui/confirmContext.ts';
import { useToast } from '../../ui/toastContext.ts';
import { useDeletePlaybook } from '../../api/hooks/usePlaybooks.ts';
import { useRepos } from '../../api/hooks/useRepos.ts';
import type { Playbook, PlaybookOutline, Template } from '../../api/types.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { may } from '../../lib/roles.ts';
import { playbookLabel } from './playbookColumns.tsx';
import { PlaybookSourcePill } from './PlaybookSourcePill.tsx';
import { RunPlaybookButton } from './RunPlaybookButton.tsx';
import type { YAMLProblem } from './validate.ts';

interface PlaybookHeaderProps {
  org: string;
  playbook: Playbook;
  role: string;
  /** The templates using the playbook; undefined while they are read. */
  templates: readonly Template[] | undefined;
  /** The templates could not be read: Run… and Delete stay disabled and
   * say why. */
  templatesFailed: boolean;
  /** The preview's problem with the draft (null when it passes, and for a
   * repository playbook, whose text the page does not have). */
  problem: YAMLProblem | null;
  dirty: boolean;
  canSave: boolean;
  onSave: () => void;
  /** Called before a deleted playbook's page is left: its draft went with
   * it, so leaving must not ask about it. */
  onLeave: () => Promise<void>;
  /** The draft's outline (editor playbooks): its counts join the summary
   * line, its tags show as chips. */
  outline: PlaybookOutline | null;
  /** Opens the Dry run tab. */
  onDryRun: () => void;
}

/**
 * PlaybookHeader is the top of the playbook page: the name (monospaced),
 * the state — Valid or the preview's problem, Unsaved while the draft
 * differs, and the source (the editor, or the repository and branch) —
 * the summary line (with the outline's counts of plays, tasks and handlers),
 * the outline's tags, and the actions Dry run… (to the Dry run tab), Run…,
 * Save and the "⋯" menu with
 * Delete…, which is refused while a template uses the playbook. Both need
 * the templates: when they cannot be read, a line under the actions says
 * so and describes the disabled Run….
 */
export function PlaybookHeader({ org, playbook, role, templates, templatesFailed, problem, dirty, canSave, onSave, onLeave, outline, onDryRun }: PlaybookHeaderProps) {
  const t = useT();
  const p = t.playbooks.page;
  const fmt = useFormat();
  const repos = useRepos(org);
  const name = playbookLabel(playbook, t);
  const repo = (repos.data?.repos ?? []).find((r) => r.id === playbook.repo_id);
  const editorSource = playbook.source === 'editor';
  const source = editorSource ? p.sourceEditor : p.sourceRepo(repo?.name ?? playbook.repo_id?.slice(0, 8) ?? '', repo?.branch ?? '…');
  // A repository playbook's summary is its path, which is already the name.
  const summary = playbook.summary && playbook.summary !== name ? playbook.summary : '';
  const hintId = useId();
  const canRun = may(role, 'jobs.run');
  const canDelete = may(role, 'playbooks.write');
  const counts = outline ? p.counts(outline.counts.plays, outline.counts.tasks, outline.counts.handlers) : '';
  const line = [summary, counts, playbook.updated_at ? p.updated(fmt.relative(playbook.updated_at)) : ''].filter(Boolean).join(' · ');
  const tags = outline?.tags ?? [];

  return (
    <div data-testid="playbook-header" className="flex flex-col gap-4 md:flex-row md:items-start md:justify-between">
      <div className="flex min-w-0 flex-1 flex-col gap-2">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1.5">
          <h1 className="min-w-0 font-mono text-xl font-bold break-all">{name}</h1>
          {editorSource && (problem ? <Pill tone="crit">{problem.message}</Pill> : <Pill tone="ok">{p.valid}</Pill>)}
          {dirty && <Pill tone="warn">{p.unsaved}</Pill>}
          <PlaybookSourcePill source={playbook.source} label={source} />
        </div>
        {line && <p className="text-[13px] text-muted-foreground">{line}</p>}
        {tags.length > 0 && (
          <div className="flex min-w-0 flex-wrap items-center gap-1.5 text-[11px] text-muted-foreground">
            <span aria-hidden="true">{p.tags}:</span>
            <ul aria-label={p.tags} className="flex flex-wrap gap-1">
              {tags.map((tag) => (
                <li key={tag}>
                  <Tag>{tag}</Tag>
                </li>
              ))}
            </ul>
          </div>
        )}
      </div>
      <div className="flex shrink-0 flex-col gap-1 md:items-end">
        <div className="flex flex-wrap items-center gap-2">
          {canRun && (
            <Button onClick={onDryRun}>
              <FlaskConical size={14} /> {p.dryRunButton}
            </Button>
          )}
          {canRun && (
            <RunPlaybookButton
              org={org}
              playbookId={playbook.id}
              name={name}
              templates={templates}
              describedBy={templatesFailed ? hintId : undefined}
              dirty={dirty}
              canCreateTemplate={may(role, 'templates.write')}
            />
          )}
          {editorSource && canDelete && (
            <Button variant="primary" disabled={!canSave} title={t.playbooks.editor.saveShortcut} aria-keyshortcuts="Control+S Meta+S" onClick={onSave}>
              <Save size={14} /> {p.save}
            </Button>
          )}
          {canDelete && <DeleteMenu org={org} playbook={playbook} name={name} templates={templates} templatesFailed={templatesFailed} onLeave={onLeave} />}
        </div>
        {templatesFailed && (canRun || canDelete) && (
          <p id={hintId} className="text-[11px] text-destructive">
            {p.templatesFailed}
          </p>
        )}
      </div>
    </div>
  );
}

/** The "⋯" menu with Delete…: refused, with the reason in the item, while a
 * template uses the playbook (the server refuses it too). */
function DeleteMenu({
  org,
  playbook,
  name,
  templates,
  templatesFailed,
  onLeave,
}: {
  org: string;
  playbook: Playbook;
  name: string;
  templates: readonly Template[] | undefined;
  templatesFailed: boolean;
  onLeave: () => Promise<void>;
}) {
  const t = useT();
  const p = t.playbooks.page;
  const e = t.playbooks.editor;
  const ask = useConfirm();
  const toast = useToast();
  const navigate = useNavigate();
  const remove = useDeletePlaybook(org);
  const inUse = templates?.length ?? 0;
  // Until the templates are known, whether any uses the playbook is not.

  async function onDelete() {
    if (!(await ask({ message: e.confirmDelete(name), confirmLabel: t.common.delete, destructive: true }))) return;
    try {
      await remove.mutateAsync(playbook.id);
      toast.show(e.deleted);
      await onLeave();
      await navigate({ to: '/o/$org/playbooks', params: { org } });
      remove.forget(playbook.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : e.deleteFailed, 'destructive');
    }
  }

  return (
    <RowMenu label={p.more(name)} triggerClassName="h-[34px] w-[34px] rounded-md border border-border bg-card text-foreground hover:bg-accent">
      <RowMenuItem destructive disabled={!templates || inUse > 0 || remove.isPending} onSelect={() => void onDelete()}>
        <span className="block">{p.delete}</span>
        {inUse > 0 && <span className="block text-[11px] font-normal text-muted-foreground">{p.deleteInUse(inUse)}</span>}
        {templatesFailed && <span className="block text-[11px] font-normal text-muted-foreground">{p.templatesFailed}</span>}
      </RowMenuItem>
    </RowMenu>
  );
}
