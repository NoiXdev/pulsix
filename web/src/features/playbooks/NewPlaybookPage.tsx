import { useId, useState, type KeyboardEvent } from 'react';
import { Link, useNavigate, useParams } from '@tanstack/react-router';
import { Button } from '../../ui/button.tsx';
import { buttonClass } from '../../ui/buttonStyles.ts';
import { CodeEditor } from '../../ui/code-editor.tsx';
import { useConfirm } from '../../ui/confirmContext.ts';
import { useToast } from '../../ui/toastContext.ts';
import { useUnsavedGuard } from '../../ui/useUnsavedGuard.ts';
import { useCreatePlaybook } from '../../api/hooks/usePlaybooks.ts';
import { usePickRepoPlaybook } from '../../api/hooks/useRepos.ts';
import { useT } from '../../i18n/useT.ts';
import { cn } from '../../lib/utils.ts';
import { NewPlaybookRepoSource, type RepoChoice } from './NewPlaybookRepoSource.tsx';
import { PlaybookCheckPanel, SidePanel } from './PlaybookSidePanels.tsx';
import { firstPlayName } from './playName.ts';
import { PLAYBOOK_STARTERS, type PlaybookStarter } from './starters.ts';
import { validateYAMLProblem } from './validate.ts';

type Source = 'editor' | 'repo';

const EDITOR_HEIGHT = 'max(22rem, calc(100vh - 22rem))';

/**
 * NewPlaybookPage adds a playbook (spec 2026-11-19-playbook-pages-design.md
 * §1, "New page"): written in the editor — from scratch or from one of four
 * starters, named by its first play — or chosen from a repository's
 * discovered playbooks (the template form's repository picker, on a page).
 * Create opens the new playbook's page. Leaving with text in the editor
 * asks first, unless it is a starter left as it was.
 */
export function NewPlaybookPage() {
  const t = useT();
  const n = t.playbooks.newPage;
  const { org } = useParams({ strict: false }) as { org: string };
  const navigate = useNavigate();
  const toast = useToast();
  const ask = useConfirm();
  const labelId = useId();
  const [source, setSource] = useState<Source>('editor');
  const [text, setText] = useState('');
  const [starterText, setStarterText] = useState('');
  const [repoChoice, setRepoChoice] = useState<RepoChoice>({ repoId: '', path: '' });
  const [serverError, setServerError] = useState<string | null>(null);
  const create = useCreatePlaybook(org);
  const pick = usePickRepoPlaybook(org, repoChoice.repoId);

  const edited = text !== '' && text !== starterText;
  const release = useUnsavedGuard(source === 'editor' && edited);
  const empty = text.trim() === '';
  const problem = empty ? null : validateYAMLProblem(text, t);
  const pending = create.isPending || pick.isPending;
  const ready = source === 'editor' ? !empty && problem === null : !!repoChoice.repoId && !!repoChoice.path;
  const name = firstPlayName(text);

  async function applyStarter(s: PlaybookStarter) {
    if (edited && !(await ask({ title: n.replaceTitle, message: n.replaceBody, confirmLabel: n.replace }))) return;
    setText(s.yaml);
    setStarterText(s.yaml);
  }

  async function submit() {
    if (!ready || pending) return;
    setServerError(null);
    try {
      let id: string;
      if (source === 'editor') {
        id = (await create.mutateAsync({ inline: text })).playbook.id;
        toast.show(t.playbooks.editor.created);
      } else {
        const res = await pick.mutateAsync(repoChoice.path);
        id = res.playbook.id;
        toast.show(res.created ? t.playbooks.editor.created : n.alreadyRegistered);
      }
      await release();
      await navigate({ to: '/o/$org/playbooks/$id', params: { org, id }, search: { tab: undefined } });
    } catch (err) {
      const message = err instanceof Error ? err.message : t.playbooks.editor.saveFailed;
      if (source === 'editor') setServerError(message);
      else toast.show(message, 'destructive');
    }
  }

  function onKeyDown(ev: KeyboardEvent<HTMLDivElement>) {
    if (!(ev.ctrlKey || ev.metaKey) || ev.altKey || ev.key.toLowerCase() !== 's') return;
    ev.preventDefault();
    void submit();
  }

  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
        <h1 className="text-xl font-semibold">{n.title}</h1>
        <div className="flex gap-2">
          <Link to="/o/$org/playbooks" params={{ org }} className={buttonClass('default')}>
            {t.common.cancel}
          </Link>
          <Button variant="primary" disabled={!ready || pending} onClick={() => void submit()}>
            {n.create}
          </Button>
        </div>
      </div>

      <SourceChoice value={source} onChange={setSource} />

      {source === 'repo' ? (
        <NewPlaybookRepoSource org={org} value={repoChoice} onChange={setRepoChoice} />
      ) : (
        <div className="grid grid-cols-1 items-start gap-5 min-[1100px]:grid-cols-[minmax(0,1fr)_300px]">
          <div className="flex min-w-0 flex-col gap-3" onKeyDown={onKeyDown}>
            <div className="flex flex-col gap-0.5">
              <span className="text-[11px] font-medium text-muted-foreground">{n.name}</span>
              <output className={cn('font-mono text-sm', !name && 'text-muted-foreground')}>{name || n.unnamed}</output>
              <span className="text-[11px] text-muted-foreground">{n.nameHint}</span>
            </div>
            <div className="flex flex-col gap-1">
              <span id={labelId} className="text-[11px] font-medium text-muted-foreground">
                {t.playbooks.editor.yaml}
              </span>
              <CodeEditor
                labelledBy={labelId}
                autoFocus
                language="yaml"
                value={text}
                onChange={setText}
                invalid={problem !== null}
                height={EDITOR_HEIGHT}
                diagnostics={problem ? [problem] : []}
              />
            </div>
          </div>
          <aside className="flex min-w-0 flex-col gap-4">
            <SidePanel title={n.starters.title}>
              <p className="text-[11px] text-muted-foreground">{n.starters.hint}</p>
              <div className="flex flex-col gap-1.5">
                {PLAYBOOK_STARTERS.map((s) => (
                  <button
                    key={s.id}
                    type="button"
                    onClick={() => void applyStarter(s)}
                    className="flex flex-col items-start rounded-md border border-border bg-background px-3 py-2 text-left hover:border-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
                  >
                    <span className="text-xs font-semibold">{n.starters[s.id].label}</span>
                    <span className="text-[11px] text-muted-foreground">{n.starters[s.id].description}</span>
                  </button>
                ))}
              </div>
            </SidePanel>
            <PlaybookCheckPanel problem={problem} serverError={serverError} idle={empty ? n.emptyCheck : undefined} />
            <p className="text-[11px] text-muted-foreground">{n.afterCreate}</p>
          </aside>
        </div>
      )}
    </div>
  );
}

/** The two ways to add a playbook, as two radio cards. */
function SourceChoice({ value, onChange }: { value: Source; onChange: (s: Source) => void }) {
  const t = useT();
  const n = t.playbooks.newPage;
  const name = useId();
  const options: { value: Source; label: string; hint: string }[] = [
    { value: 'editor', label: n.sourceEditor, hint: n.sourceEditorHint },
    { value: 'repo', label: n.sourceRepo, hint: n.sourceRepoHint },
  ];
  return (
    <fieldset className="flex min-w-0 flex-col gap-2">
      <legend className="mb-2 text-[11px] font-medium text-muted-foreground">{n.source}</legend>
      <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
        {options.map((o) => (
          <label
            key={o.value}
            className={cn(
              'flex cursor-pointer flex-col gap-0.5 rounded-[10px] border px-4 py-3 has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-primary',
              o.value === value ? 'border-primary bg-primary/10' : 'border-border bg-card hover:border-primary/60',
            )}
          >
            <input type="radio" className="sr-only" name={name} value={o.value} checked={o.value === value} onChange={() => onChange(o.value)} />
            <span className="text-[13px] font-semibold">{o.label}</span>
            <span className="text-xs text-muted-foreground">{o.hint}</span>
          </label>
        ))}
      </div>
    </fieldset>
  );
}
