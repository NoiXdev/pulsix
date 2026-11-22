import { useId, useRef } from 'react';
import { CodeEditor, type CodeEditorHandle } from '../../ui/code-editor.tsx';
import { DisabledHint } from '../../ui/detail-layout.tsx';
import { useRepos } from '../../api/hooks/useRepos.ts';
import type { Playbook, PlaybookOutline, Template } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { PlaybookCheckPanel, PlaybookVariablesPanel } from './PlaybookSidePanels.tsx';
import { PlaybookLatestRuns } from './PlaybookLatestRuns.tsx';
import { PlaybookOutlinePanel } from './PlaybookOutlinePanel.tsx';
import type { YAMLProblem } from './validate.ts';

/** The editor's height: the page's own, less the shell's bar, the header
 * and the tab strip above it. */
const EDITOR_HEIGHT = 'max(24rem, calc(100vh - 17rem))';

interface PlaybookEditorTabProps {
  org: string;
  playbook: Playbook;
  canWrite: boolean;
  draft: string;
  onChange: (text: string) => void;
  problem: YAMLProblem | null;
  /** The server's refusal of the last save, if it refused it. */
  serverError: string | null;
  templates: readonly Template[];
  onShowRuns: () => void;
  /** The draft's outline (usePlaybookOutline) and whether it is the last
   * good one of a draft that does not parse now. */
  outline: PlaybookOutline | null;
  outlineStale: boolean;
}

/**
 * PlaybookEditorTab is the playbook page's Editor tab: the YAML in the code
 * editor at page height, and beside it the Check (the preview's message and
 * line, and the server's refusal of a save), the Variables hint and the
 * latest runs; Ctrl/Cmd-S is the page's own (PlaybookPage). A repository
 * playbook has no text in Pulsix: a note says where it lives and that it
 * changes through Git. A reader gets the text read-only.
 */
export function PlaybookEditorTab({ org, playbook, canWrite, draft, onChange, problem, serverError, templates, onShowRuns, outline, outlineStale }: PlaybookEditorTabProps) {
  const t = useT();
  const labelId = useId();
  const editor = useRef<CodeEditorHandle>(null);
  const latest = <PlaybookLatestRuns org={org} playbookId={playbook.id} templates={templates} onShowAll={onShowRuns} />;

  if (playbook.source === 'repo') {
    return (
      <div className="grid grid-cols-1 items-start gap-5 min-[1100px]:grid-cols-[minmax(0,1fr)_300px]">
        <RepoNote org={org} playbook={playbook} />
        <aside className="flex min-w-0 flex-col gap-4">
          <PlaybookOutlinePanel outline={null} repo />
          {latest}
        </aside>
      </div>
    );
  }

  return (
    <div className="grid grid-cols-1 items-start gap-5 min-[1100px]:grid-cols-[minmax(0,1fr)_300px]">
      <div className="flex min-w-0 flex-col gap-1">
        <span id={labelId} className="text-[11px] font-medium text-muted-foreground">
          {t.playbooks.editor.yaml}
        </span>
        <CodeEditor
          ref={editor}
          labelledBy={labelId}
          language="yaml"
          value={draft}
          onChange={canWrite ? onChange : undefined}
          readOnly={!canWrite}
          invalid={problem !== null}
          height={EDITOR_HEIGHT}
          diagnostics={problem ? [problem] : []}
        />
        {!canWrite && <p className="text-[11px] text-muted-foreground">{t.playbooks.page.readOnly}</p>}
      </div>
      <aside className="flex min-w-0 flex-col gap-4">
        <PlaybookCheckPanel problem={problem} serverError={serverError} />
        <PlaybookOutlinePanel outline={outline} stale={outlineStale} onReveal={(line) => editor.current?.revealLine(line)} />
        <PlaybookVariablesPanel yaml={draft} />
        {latest}
      </aside>
    </div>
  );
}

/** Where a repository playbook lives, in place of an editor. */
function RepoNote({ org, playbook }: { org: string; playbook: Playbook }) {
  const t = useT();
  const repos = useRepos(org);
  const repo = (repos.data?.repos ?? []).find((r) => r.id === playbook.repo_id);
  return (
    <DisabledHint
      title={t.playbooks.page.repoNoteTitle}
      body={t.playbooks.page.repoNote(playbook.path ?? '', repo?.name ?? playbook.repo_id?.slice(0, 8) ?? '', repo?.branch ?? '…')}
    />
  );
}
