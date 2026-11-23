import { useId, useRef } from 'react';
import { CodeEditor, type CodeEditorHandle } from '../../ui/code-editor.tsx';
import type { Playbook, PlaybookOutline, Template } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { EDITOR_GRID, EDITOR_HEIGHT } from './editorLayout.ts';
import { PlaybookCheckPanel, PlaybookVariablesPanel } from './PlaybookSidePanels.tsx';
import { PlaybookLatestRuns } from './PlaybookLatestRuns.tsx';
import { PlaybookOutlinePanel } from './PlaybookOutlinePanel.tsx';
import { PlaybookRepoTextView } from './PlaybookRepoTextView.tsx';
import type { OutlineProblem } from './usePlaybookOutline.ts';
import type { YAMLProblem } from './validate.ts';

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
  outlineProblem: OutlineProblem | null;
}

/**
 * PlaybookEditorTab is the playbook page's Editor tab: the YAML in the code
 * editor at page height, and beside it the Check (the preview's message and
 * line, and the server's refusal of a save), the Variables hint and the
 * latest runs; Ctrl/Cmd-S is the page's own (PlaybookPage). A repository
 * playbook shows its text as of the last sync, read-only
 * (PlaybookRepoTextView). A reader gets the text read-only.
 */
export function PlaybookEditorTab({ org, playbook, canWrite, draft, onChange, problem, serverError, templates, onShowRuns, outline, outlineProblem }: PlaybookEditorTabProps) {
  const t = useT();
  const labelId = useId();
  const editor = useRef<CodeEditorHandle>(null);
  const latest = <PlaybookLatestRuns org={org} playbookId={playbook.id} templates={templates} onShowAll={onShowRuns} />;

  if (playbook.source === 'repo') return <PlaybookRepoTextView org={org} playbook={playbook} latest={latest} />;

  return (
    <div className={EDITOR_GRID}>
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
        <PlaybookOutlinePanel outline={outline} problem={outlineProblem} onReveal={(line) => editor.current?.revealLine(line)} />
        <PlaybookVariablesPanel yaml={draft} />
        {latest}
      </aside>
    </div>
  );
}
