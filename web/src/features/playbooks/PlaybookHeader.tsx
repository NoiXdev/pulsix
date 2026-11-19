import { useNavigate } from '@tanstack/react-router';
import { Save } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { Pill } from '../../ui/badge.tsx';
import { RowMenu, RowMenuItem } from '../../ui/list-row.tsx';
import { useConfirm } from '../../ui/confirmContext.ts';
import { useToast } from '../../ui/toastContext.ts';
import { useDeletePlaybook } from '../../api/hooks/usePlaybooks.ts';
import { useRepos } from '../../api/hooks/useRepos.ts';
import type { Playbook, Template } from '../../api/types.ts';
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
  /** The preview's problem with the draft (null when it passes, and for a
   * repository playbook, whose text the page does not have). */
  problem: YAMLProblem | null;
  dirty: boolean;
  canSave: boolean;
  onSave: () => void;
  /** Called before a deleted playbook's page is left: its draft went with
   * it, so leaving must not ask about it. */
  onLeave: () => Promise<void>;
}

/**
 * PlaybookHeader is the top of the playbook page: the name (monospaced),
 * the state — Valid or the preview's problem, Unsaved while the draft
 * differs, and the source (the editor, or the repository and branch) —
 * the summary line, and the actions Run…, Save and the "⋯" menu with
 * Delete…, which is refused while a template uses the playbook.
 */
export function PlaybookHeader({ org, playbook, role, templates, problem, dirty, canSave, onSave, onLeave }: PlaybookHeaderProps) {
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
  const line = [summary, playbook.updated_at ? p.updated(fmt.relative(playbook.updated_at)) : ''].filter(Boolean).join(' · ');

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
      </div>
      <div className="flex shrink-0 flex-wrap items-center gap-2">
        {may(role, 'jobs.run') && <RunPlaybookButton org={org} playbookId={playbook.id} name={name} templates={templates} canCreateTemplate={may(role, 'templates.write')} />}
        {editorSource && may(role, 'playbooks.write') && (
          <Button variant="primary" disabled={!canSave} title={t.playbooks.editor.saveShortcut} aria-keyshortcuts="Control+S Meta+S" onClick={onSave}>
            <Save size={14} /> {p.save}
          </Button>
        )}
        {may(role, 'playbooks.write') && <DeleteMenu org={org} playbook={playbook} name={name} templates={templates} onLeave={onLeave} />}
      </div>
    </div>
  );
}

/** The "⋯" menu with Delete…: refused, with the reason in the item, while a
 * template uses the playbook (the server refuses it too). */
function DeleteMenu({ org, playbook, name, templates, onLeave }: { org: string; playbook: Playbook; name: string; templates: readonly Template[] | undefined; onLeave: () => Promise<void> }) {
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
      </RowMenuItem>
    </RowMenu>
  );
}
