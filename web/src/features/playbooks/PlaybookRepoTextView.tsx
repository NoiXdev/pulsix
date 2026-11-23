import { useId, useMemo, useRef, type ReactNode } from 'react';
import { Link } from '@tanstack/react-router';
import { CodeEditor, type CodeEditorHandle } from '../../ui/code-editor.tsx';
import { DisabledHint } from '../../ui/detail-layout.tsx';
import { useRepoPlaybooks, useRepos } from '../../api/hooks/useRepos.ts';
import type { Playbook, Repo } from '../../api/types.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { EDITOR_GRID, EDITOR_HEIGHT } from './editorLayout.ts';
import { PlaybookCheckPanel, PlaybookVariablesPanel } from './PlaybookSidePanels.tsx';
import { PlaybookOutlinePanel } from './PlaybookOutlinePanel.tsx';
import { usePlaybookOutline } from './usePlaybookOutline.ts';
import { usePlaybookRepoText, type RepoText } from './usePlaybookRepoText.ts';
import { validateYAMLProblem } from './validate.ts';

interface PlaybookRepoTextViewProps {
  org: string;
  playbook: Playbook;
  /** The latest runs panel, last in the side column. */
  latest: ReactNode;
}

/**
 * PlaybookRepoTextView is the Editor tab of a repository playbook (backlog
 * 3.127): its text as of the last sync in a read-only editor, under a line
 * with the sync's time and commit and a link to the repository, and beside
 * it the Check, the Outline and the Variables of that text, these with
 * what the sync read from its variable sources. Without a text the note
 * says where the playbook lives, and why there is none.
 */
export function PlaybookRepoTextView({ org, playbook, latest }: PlaybookRepoTextViewProps) {
  const text = usePlaybookRepoText(org, playbook.id, true);
  const repos = useRepos(org);
  const repo = (repos.data?.repos ?? []).find((r) => r.id === playbook.repo_id);
  if (text.state === 'text') return <RepoTextEditor org={org} playbook={playbook} repo={repo} text={text} latest={latest} />;
  return (
    <div className={EDITOR_GRID}>
      <RepoNote playbook={playbook} repo={repo} text={text} />
      <aside className="flex min-w-0 flex-col gap-4">
        <PlaybookOutlinePanel outline={null} repo={text.state} />
        {latest}
      </aside>
    </div>
  );
}

type Shown = Extract<RepoText, { state: 'text' }>;

function RepoTextEditor({ org, playbook, repo, text, latest }: { org: string; playbook: Playbook; repo: Repo | undefined; text: Shown; latest: ReactNode }) {
  const t = useT();
  const r = t.playbooks.page.repoText;
  const fmt = useFormat();
  const labelId = useId();
  const editor = useRef<CodeEditorHandle>(null);
  const problem = useMemo(() => validateYAMLProblem(text.content, t), [text.content, t]);
  const { outline, invalid } = usePlaybookOutline(org, text.content, true);
  const when = fmt.dateTime(text.syncedAt);
  return (
    <div className={EDITOR_GRID}>
      <div className="flex min-w-0 flex-col gap-1">
        <p className="text-xs text-muted-foreground">
          <span>{text.commit ? r.asOf(when, text.commit.slice(0, 7)) : r.earlier(when)}</span>
          {' · '}
          <RepoLink org={org} playbook={playbook} repo={repo} />
        </p>
        <span id={labelId} className="text-[11px] font-medium text-muted-foreground">
          {t.playbooks.editor.yaml}
        </span>
        <CodeEditor
          ref={editor}
          labelledBy={labelId}
          language="yaml"
          value={text.content}
          readOnly
          invalid={problem !== null}
          height={EDITOR_HEIGHT}
          diagnostics={problem ? [problem] : []}
        />
        <p className="text-[11px] text-muted-foreground">{r.readOnly}</p>
      </div>
      <aside className="flex min-w-0 flex-col gap-4">
        <PlaybookCheckPanel problem={problem} serverError={null} ready={r.ready} hint={r.checkHint} />
        <PlaybookOutlinePanel outline={outline} stale={invalid} onReveal={(line) => editor.current?.revealLine(line)} />
        <PlaybookVariablesPanel yaml={text.content} stored={<StoredSources org={org} playbook={playbook} />} />
        {latest}
      </aside>
    </div>
  );
}

/** The repository, linked to its row in the repositories list. */
function RepoLink({ org, playbook, repo }: { org: string; playbook: Playbook; repo: Repo | undefined }) {
  const t = useT();
  if (!repo) return <span>{playbook.repo_id?.slice(0, 8)}</span>;
  return (
    <Link to="/o/$org/repos" params={{ org }} search={{ q: repo.name }} className="font-medium text-primary hover:underline">
      {t.playbooks.page.sourceRepo(repo.name, repo.branch)}
    </Link>
  );
}

/** What the last sync read from the playbook's variable sources, as the
 * repository's list of discovered playbooks records it for its path. */
function StoredSources({ org, playbook }: { org: string; playbook: Playbook }) {
  const t = useT();
  const s = t.playbooks.page.repoText.sources;
  const discovered = useRepoPlaybooks(org, playbook.repo_id ?? '');
  if (!discovered.data) return null;
  const entry = (discovered.data.playbooks ?? []).find((d) => d.path === playbook.path);
  const message = !discovered.data.sources || !entry ? s.none : entry.failed ? s.failed : entry.left_out ? s.leftOut(entry.fields) : s.read(entry.fields);
  return <p className="text-xs">{message}</p>;
}

/** Where a repository playbook lives, in place of an editor, and why the
 * last sync stored no text for it. */
function RepoNote({ playbook, repo, text }: { playbook: Playbook; repo: Repo | undefined; text: Exclude<RepoText, Shown> }) {
  const t = useT();
  const r = t.playbooks.page.repoText;
  const reason =
    text.state === 'loading' ? r.loading : text.state === 'not-synced' ? r.notSynced : text.state === 'failed' ? r.readFailed(text.message) : r.leftOut[text.reason];
  return (
    <DisabledHint
      title={t.playbooks.page.repoNoteTitle}
      body={
        <>
          {t.playbooks.page.repoNote(playbook.path ?? '', repo?.name ?? playbook.repo_id?.slice(0, 8) ?? '', repo?.branch ?? '…')}
          <span className="mt-1 block">{reason}</span>
        </>
      }
    />
  );
}
