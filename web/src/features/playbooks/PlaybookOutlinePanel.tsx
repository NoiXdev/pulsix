import { TriangleAlert } from 'lucide-react';
import { Tag } from '../../ui/badge.tsx';
import type { OutlineEntry, OutlinePlay, PlaybookOutline } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import { useT } from '../../i18n/useT.ts';
import { outlineEntryName } from './outlineNames.ts';
import { SidePanel } from './PlaybookSidePanels.tsx';
import type { OutlineProblem } from './usePlaybookOutline.ts';
import type { RepoNoText } from './usePlaybookRepoText.ts';

type RepoOutlineNotes = Messages['playbooks']['page']['outline']['repo'];

/** Each state without a text, to its sentence. */
const REPO_NOTE: Record<RepoNoText, (r: RepoOutlineNotes) => string> = {
  loading: (r) => r.loading,
  'not-synced': (r) => r.notSynced,
  'no-route': (r) => r.noRoute,
  failed: (r) => r.failed,
  'left-out': (r) => r.leftOut,
};

/** At most this many tags show on an entry; the rest as "+n". */
const MAX_ENTRY_TAGS = 3;

interface PlaybookOutlinePanelProps {
  /** The outline, null before the first one arrived (or for a repository playbook). */
  outline: PlaybookOutline | null;
  /** Why `outline` is not fresh: the text does not parse right now, or the
   * request failed; `outline` is the last one that was read. */
  problem?: OutlineProblem | null;
  /** A repository playbook without a text to outline, and why: its text is
   * loading, the repository was never synced, the read failed, or the last
   * sync stored none. */
  repo?: RepoNoText;
  /** Puts the editor's cursor on a line and focuses it. */
  onReveal?: (line: number) => void;
}

/**
 * PlaybookOutlinePanel is the Editor tab's Outline (spec §4): each play's
 * line (name, hosts, become), then its tasks, blocks, includes, roles and
 * handlers with line, name and tags, indented by block depth, under the
 * section they are in. A click puts the editor's cursor on the entry's
 * line. An entry that runs for real in a dry run (check_mode: false on it
 * or around it) carries a warning mark.
 */
export function PlaybookOutlinePanel({ outline, problem, repo, onReveal }: PlaybookOutlinePanelProps) {
  const t = useT();
  const o = t.playbooks.page.outline;
  const plays = outline?.plays ?? [];
  return (
    <SidePanel title={o.title}>
      {repo ? (
        <p className="text-xs text-muted-foreground">{REPO_NOTE[repo](o.repo)}</p>
      ) : plays.length === 0 ? (
        <>
          {problem && <p className="text-[11px]" style={{ color: 'var(--px-warn)' }}>{problem === 'invalid' ? o.staleNone : o.unavailableNone}</p>}
          {!problem && <p className="text-xs text-muted-foreground">{o.none}</p>}
        </>
      ) : (
        <>
          {problem && <p className="text-[11px]" style={{ color: 'var(--px-warn)' }}>{problem === 'invalid' ? o.stale : o.unavailable}</p>}
          {plays.map((play, i) => (
            <PlayOutline key={`${play.line}-${i}`} play={play} onReveal={onReveal} />
          ))}
          {outline?.truncated && <p className="text-[11px] text-muted-foreground">{o.truncated}</p>}
          <p className="text-[11px] text-muted-foreground">{o.hint}</p>
        </>
      )}
    </SidePanel>
  );
}

const ROW = 'flex w-full min-w-0 items-start gap-2 rounded px-1 py-0.5 text-left text-xs hover:bg-accent focus-visible:outline-2 focus-visible:outline-primary';

function PlayOutline({ play, onReveal }: { play: OutlinePlay; onReveal?: (line: number) => void }) {
  const t = useT();
  const o = t.playbooks.page.outline;
  const name = play.name || o.unnamed;
  const entries = play.entries ?? [];
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <button type="button" className={ROW} aria-label={o.go(play.line, name)} onClick={() => onReveal?.(play.line)}>
        <span className="flex min-w-0 flex-col">
          <span className="font-semibold break-words">{name}</span>
          <span className="font-mono text-[11px] text-muted-foreground break-all">{[play.hosts, play.become ? o.become : ''].filter(Boolean).join(' · ')}</span>
        </span>
      </button>
      <ul className="flex min-w-0 flex-col">
        {entries.map((e, i) => (
          <li key={`${e.line}-${i}`} className="min-w-0">
            {(i === 0 || entries[i - 1].section !== e.section) && (
              <span className="mt-1 block px-1 font-mono text-[10px] tracking-wide text-muted-foreground uppercase">{e.section}</span>
            )}
            <EntryRow entry={e} onReveal={onReveal} />
          </li>
        ))}
      </ul>
    </div>
  );
}

function EntryRow({ entry, onReveal }: { entry: OutlineEntry; onReveal?: (line: number) => void }) {
  const t = useT();
  const o = t.playbooks.page.outline;
  const name = outlineEntryName(entry, o);
  const real = !entry.runs_in_check_mode;
  const tags = entry.tags ?? [];
  return (
    <button
      type="button"
      className={ROW}
      style={{ paddingLeft: `${0.25 + Math.min(entry.depth, 8) * 0.75}rem` }}
      aria-label={real ? o.goReal(entry.line, name) : o.go(entry.line, name)}
      onClick={() => onReveal?.(entry.line)}
    >
      <span className="w-9 shrink-0 font-mono text-[11px] text-muted-foreground">{o.line(entry.line)}</span>
      <span className="flex min-w-0 flex-1 flex-wrap items-center gap-1">
        <span className="break-words">{name}</span>
        {real && (
          <span title={o.real} className="inline-flex" style={{ color: 'var(--px-warn)' }}>
            <TriangleAlert size={12} aria-hidden />
          </span>
        )}
        {tags.slice(0, MAX_ENTRY_TAGS).map((tag) => (
          <Tag key={tag} className="px-1 py-0 text-[10px]">
            {tag}
          </Tag>
        ))}
        {tags.length > MAX_ENTRY_TAGS && <span className="text-[10px] text-muted-foreground">+{tags.length - MAX_ENTRY_TAGS}</span>}
      </span>
    </button>
  );
}
